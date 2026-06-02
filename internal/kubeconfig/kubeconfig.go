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

	host, err := endpointHost(cluster.Server)
	if err != nil {
		return ClusterInfo{}, err
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

	return ClusterInfo{
		ContextName:     ctxName,
		PrivateEndpoint: host,
		ClusterOCID:     ocid,
		Region:          region,
	}, nil
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

// execArg returns the value following flag in an oci exec credential's args,
// supporting both "--flag value" and "--flag=value" forms.
func execArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if len(a) > len(flag)+1 && a[:len(flag)+1] == flag+"=" {
			return a[len(flag)+1:]
		}
	}
	return ""
}
