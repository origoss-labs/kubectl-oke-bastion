// Package kubeconfig reads OKE cluster facts from a kubeconfig and (later)
// manages the -bastion context the plugin wires to the local tunnel.
package kubeconfig

import (
	"fmt"
	"net/url"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ClusterInfo holds the OKE cluster facts derived from a kubeconfig's current
// context: the private endpoint the tunnel targets, and the cluster OCID and
// region carried by the oci exec credential.
type ClusterInfo struct {
	ContextName     string
	PrivateEndpoint string
	ClusterOCID     string
	Region          string
}

// Parse reads kubeconfig YAML and extracts the current context's OKE cluster
// facts.
func Parse(raw []byte) (ClusterInfo, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return ClusterInfo{}, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	return clusterFromConfig(cfg)
}

// Current loads the kubeconfig via the standard loading rules (honoring the
// KUBECONFIG environment variable and default path) and extracts the current
// context's OKE cluster facts.
func Current() (ClusterInfo, error) {
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return ClusterInfo{}, fmt.Errorf("loading kubeconfig: %w", err)
	}
	return clusterFromConfig(cfg)
}

func clusterFromConfig(cfg *clientcmdapi.Config) (ClusterInfo, error) {
	ctxName := cfg.CurrentContext
	if ctxName == "" {
		return ClusterInfo{}, fmt.Errorf("kubeconfig has no current-context set; select one with `kubectl config use-context`")
	}
	kctx, ok := cfg.Contexts[ctxName]
	if !ok {
		return ClusterInfo{}, fmt.Errorf("kubeconfig current-context %q is not defined in contexts", ctxName)
	}
	cluster, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		return ClusterInfo{}, fmt.Errorf("context %q references unknown cluster %q", ctxName, kctx.Cluster)
	}
	user, ok := cfg.AuthInfos[kctx.AuthInfo]
	if !ok {
		return ClusterInfo{}, fmt.Errorf("context %q references unknown user %q", ctxName, kctx.AuthInfo)
	}

	if user.Exec == nil || user.Exec.Command != "oci" {
		return ClusterInfo{}, fmt.Errorf("current context %q is not an OKE cluster: no oci exec credential found", ctxName)
	}
	ocid := execArg(user.Exec.Args, "--cluster-id")
	region := execArg(user.Exec.Args, "--region")
	if ocid == "" {
		return ClusterInfo{}, fmt.Errorf("current context %q is not an OKE cluster: oci exec credential has no --cluster-id", ctxName)
	}
	if region == "" {
		return ClusterInfo{}, fmt.Errorf("current context %q oci exec credential has no --region", ctxName)
	}

	host, err := endpointHost(cluster.Server)
	if err != nil {
		return ClusterInfo{}, err
	}

	return ClusterInfo{
		ContextName:     ctxName,
		PrivateEndpoint: host,
		ClusterOCID:     ocid,
		Region:          region,
	}, nil
}

// BastionWiring describes the non-destructive -bastion entry to wire into a
// kubeconfig: which existing context to shadow, the private endpoint the
// cluster certificate is issued for (used as tls-server-name), and the local
// port the tunnel listens on.
type BastionWiring struct {
	OriginalContext string
	PrivateEndpoint string
	LocalPort       int
}

// AddBastionContext adds a -bastion cluster+context to cfg pointing kubectl at
// the local end of the tunnel (https://127.0.0.1:<port>) while validating TLS
// against the real cluster CA via tls-server-name (ADR-0005). The original
// context's cluster CA is reused and its user (the oci exec credential) is
// referenced unchanged, so the -bastion context still authenticates. Returns
// the new context's name.
func AddBastionContext(cfg *clientcmdapi.Config, w BastionWiring) (string, error) {
	kctx, ok := cfg.Contexts[w.OriginalContext]
	if !ok {
		return "", fmt.Errorf("original context %q is not defined in contexts", w.OriginalContext)
	}
	orig, ok := cfg.Clusters[kctx.Cluster]
	if !ok {
		return "", fmt.Errorf("context %q references unknown cluster %q", w.OriginalContext, kctx.Cluster)
	}

	clusterName := kctx.Cluster + "-bastion"
	ctxName := w.OriginalContext + "-bastion"

	bastionCluster := clientcmdapi.NewCluster()
	bastionCluster.Server = fmt.Sprintf("https://127.0.0.1:%d", w.LocalPort)
	bastionCluster.TLSServerName = w.PrivateEndpoint
	bastionCluster.CertificateAuthorityData = orig.CertificateAuthorityData
	cfg.Clusters[clusterName] = bastionCluster

	bastionCtx := clientcmdapi.NewContext()
	bastionCtx.Cluster = clusterName
	bastionCtx.AuthInfo = kctx.AuthInfo
	cfg.Contexts[ctxName] = bastionCtx

	return ctxName, nil
}

// WireBastion loads the operator's active kubeconfig (honoring KUBECONFIG and
// the default path), adds the -bastion context, and persists the change with an
// atomic write via clientcmd. The original file's other entries are preserved.
// Returns the new context's name.
func WireBastion(w BastionWiring) (string, error) {
	pathOpts := clientcmd.NewDefaultPathOptions()
	cfg, err := pathOpts.GetStartingConfig()
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	name, err := AddBastionContext(cfg, w)
	if err != nil {
		return "", err
	}
	if err := clientcmd.ModifyConfig(pathOpts, *cfg, false); err != nil {
		return "", fmt.Errorf("writing -bastion context to kubeconfig: %w", err)
	}
	return name, nil
}

// UnwireBastion loads the active kubeconfig and removes the -bastion context
// named ctxName, persisting the change. It is a no-op if the context is absent,
// so teardown is safe to run unconditionally.
func UnwireBastion(ctxName string) error {
	pathOpts := clientcmd.NewDefaultPathOptions()
	cfg, err := pathOpts.GetStartingConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	if err := RemoveBastionContext(cfg, ctxName); err != nil {
		return err
	}
	if err := clientcmd.ModifyConfig(pathOpts, *cfg, false); err != nil {
		return fmt.Errorf("removing -bastion context from kubeconfig: %w", err)
	}
	return nil
}

// RemoveBastionContext removes the -bastion context named ctxName and the
// cluster it references, restoring the kubeconfig to its pre-Add state. It is a
// no-op if the context is absent, so teardown is safe to run unconditionally.
func RemoveBastionContext(cfg *clientcmdapi.Config, ctxName string) error {
	kctx, ok := cfg.Contexts[ctxName]
	if !ok {
		return nil
	}
	delete(cfg.Clusters, kctx.Cluster)
	delete(cfg.Contexts, ctxName)
	return nil
}

// endpointHost returns the host (IP) portion of a cluster server URL.
func endpointHost(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("cluster server %q is not a valid URL: %w", server, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("cluster has no server endpoint")
	}
	return host, nil
}

// execArg returns the value following flag in an oci exec credential's args.
// `oci ce cluster create-kubeconfig` always emits the separated "--flag value"
// form, which is the only form handled here.
func execArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
