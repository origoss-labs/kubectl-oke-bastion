// Package discover is the deep core of `init`'s cluster discovery (Slice B,
// ADR-0011). It walks the tenancy's accessible compartments, fans out a
// concurrent ListClusters per compartment, aggregates the ACTIVE OKE clusters
// into one labelled list, reads the chosen cluster's facts (private endpoint +
// region), lists the bastions with access to it (auto-selecting the sole one),
// and renders the cluster's kubeconfig.
//
// All OCI access goes through the narrow Client interface — exactly the SDK
// calls discovery makes, nothing more — so every behaviour here is exercised
// against a fake, never a live tenancy. The interactive prompts and the real
// SDK client construction live in the command layer; this package is pure
// discovery logic.
package discover

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// Client is the slice of the OCI SDK discovery needs: one identity call to walk
// compartments, three container-engine calls to find/inspect clusters and mint
// a kubeconfig, and one bastion call to find access. The concrete
// *identity.IdentityClient, *containerengine.ContainerEngineClient, and
// *ocibastion.BastionClient each satisfy their respective methods; the command
// layer composes them into one value and tests supply a single fake.
type Client interface {
	ListCompartments(context.Context, identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error)
	ListClusters(context.Context, containerengine.ListClustersRequest) (containerengine.ListClustersResponse, error)
	GetCluster(context.Context, containerengine.GetClusterRequest) (containerengine.GetClusterResponse, error)
	CreateKubeconfig(context.Context, containerengine.CreateKubeconfigRequest) (containerengine.CreateKubeconfigResponse, error)
	ListBastions(context.Context, ocibastion.ListBastionsRequest) (ocibastion.ListBastionsResponse, error)
}

// Compartment is an accessible compartment in the tenancy subtree, reduced to
// the two facts discovery uses: its OCID (to scope ListClusters/ListBastions)
// and its display name (for labelling).
type Compartment struct {
	OCID string
	Name string
}

// Cluster is one ACTIVE OKE cluster found during the walk, carrying the facts
// the operator picks from and init later persists.
type Cluster struct {
	OCID            string
	Name            string
	CompartmentOCID string
	CompartmentName string
}

// Label is the one-line "<cluster> — <compartment>" the picker shows, so the
// operator can disambiguate same-named clusters across compartments.
func (c Cluster) Label() string {
	return c.Name + " — " + c.CompartmentName
}

// Facts are the cluster details read from the OCI cluster object after the
// operator picks one: the private API endpoint the tunnel targets, the region
// the bastion client must be pinned to, and the VCN that scopes bastion access.
type Facts struct {
	PrivateEndpoint string
	Region          string
	VcnID           string
}

// Bastion is one bastion with access to the chosen cluster.
type Bastion struct {
	OCID string
	Name string
}

// BastionResult is the outcome of the bastion lookup: the candidate bastions
// and whether there was exactly one (so the caller can auto-select rather than
// prompt).
type BastionResult struct {
	Bastions     []Bastion
	AutoSelected bool
}

// Progress reports how many compartments have finished their ListClusters so
// init can show a live "n/total" indicator. It is delivered through a callback
// (ProgressFunc) rather than written to a stream so tests assert on the numbers,
// not on stderr formatting.
type Progress struct {
	Done  int
	Total int
}

// ProgressFunc receives a Progress update each time a compartment completes. It
// may be called concurrently from the fan-out goroutines, so an implementation
// that touches shared state must synchronize. A nil ProgressFunc disables
// reporting.
type ProgressFunc func(Progress)

// WriterProgress adapts an io.Writer into a ProgressFunc that prints a single
// rewritten "scanning N/M compartments" line. It serializes its own writes, so
// it is safe to hand to Clusters. The mechanism (not the exact text) is what
// the discovery contract guarantees.
func WriterProgress(w io.Writer) ProgressFunc {
	var mu sync.Mutex
	return func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "\rscanning compartments %d/%d", p.Done, p.Total)
		if p.Done == p.Total {
			fmt.Fprintln(w)
		}
	}
}

// Compartments lists every accessible compartment in the tenancy subtree and
// prepends the tenancy root itself (ListCompartments never returns the root it
// is called on, yet a cluster may live directly in the root). It pages through
// OpcNextPage. A list error is wrapped and surfaced, never swallowed — a
// partial compartment list would silently hide clusters from the operator.
func Compartments(ctx context.Context, c Client, rootOCID, rootName string) ([]Compartment, error) {
	subtree := true
	out := []Compartment{{OCID: rootOCID, Name: rootName}}

	var page *string
	for {
		resp, err := c.ListCompartments(ctx, identity.ListCompartmentsRequest{
			CompartmentId:          &rootOCID,
			CompartmentIdInSubtree: &subtree,
			AccessLevel:            identity.ListCompartmentsAccessLevelAccessible,
			Page:                   page,
		})
		if err != nil {
			return nil, fmt.Errorf("listing compartments in tenancy %s: %w", rootOCID, err)
		}
		for _, item := range resp.Items {
			out = append(out, Compartment{OCID: deref(item.Id), Name: deref(item.Name)})
		}
		if resp.OpcNextPage == nil {
			break
		}
		page = resp.OpcNextPage
	}
	return out, nil
}

// Clusters fans out a ListClusters per compartment concurrently, filters each
// compartment's results to ACTIVE clusters, and aggregates them into one list
// labelled "<cluster> — <compartment>". Per-compartment policy is
// aggregate-and-report: every compartment is queried even if one fails, but the
// first failure is surfaced (wrapped, naming the compartment) rather than
// swallowed — the issue requires a list error be reported, and continuing the
// fan-out keeps one denied compartment from blinding the operator to the others
// in the same run's diagnostics. progress, if non-nil, is invoked as each
// compartment completes.
func Clusters(ctx context.Context, c Client, compartments []Compartment, progress ProgressFunc) ([]Cluster, error) {
	type result struct {
		clusters []Cluster
		err      error
	}

	total := len(compartments)
	results := make([]result, total)

	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	// Deliberate aggregate-and-report: every compartment runs to completion even
	// after one fails — no goroutine cancels ctx or signals the others to stop —
	// so the first error is reported without a partial fan-out, and one denied
	// compartment can't hide clusters discovered in the rest. Not a forgotten
	// cancellation.
	for i, comp := range compartments {
		wg.Add(1)
		go func(i int, comp Compartment) {
			defer wg.Done()
			clusters, err := listActiveClusters(ctx, c, comp)
			results[i] = result{clusters: clusters, err: err}

			// Report progress as each compartment finishes; the lock makes the
			// counter and the callback safe under the concurrent fan-out.
			mu.Lock()
			done++
			d := done
			mu.Unlock()
			if progress != nil {
				progress(Progress{Done: d, Total: total})
			}
		}(i, comp)
	}
	wg.Wait()

	var (
		all      []Cluster
		firstErr error
	)
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		all = append(all, r.clusters...)
	}
	if firstErr != nil {
		return nil, firstErr
	}

	// Deterministic order so the picker list is stable across runs.
	sort.Slice(all, func(i, j int) bool { return all[i].Label() < all[j].Label() })
	return all, nil
}

// listActiveClusters lists the ACTIVE clusters in one compartment, paging
// through OpcNextPage. The LifecycleState filter is applied server-side, but
// the result is re-checked client-side so a future SDK that ignores the filter
// can't surface a non-ACTIVE cluster.
func listActiveClusters(ctx context.Context, c Client, comp Compartment) ([]Cluster, error) {
	var (
		out  []Cluster
		page *string
	)
	for {
		resp, err := c.ListClusters(ctx, containerengine.ListClustersRequest{
			CompartmentId:  &comp.OCID,
			LifecycleState: []containerengine.ClusterLifecycleStateEnum{containerengine.ClusterLifecycleStateActive},
			Page:           page,
		})
		if err != nil {
			return nil, fmt.Errorf("listing clusters in compartment %s: %w", comp.OCID, err)
		}
		for _, cs := range resp.Items {
			if cs.LifecycleState != containerengine.ClusterLifecycleStateActive {
				continue
			}
			out = append(out, Cluster{
				OCID:            deref(cs.Id),
				Name:            deref(cs.Name),
				CompartmentOCID: comp.OCID,
				CompartmentName: comp.Name,
			})
		}
		if resp.OpcNextPage == nil {
			break
		}
		page = resp.OpcNextPage
	}
	return out, nil
}

// ClusterFacts reads the chosen cluster's object and extracts the private
// endpoint (the tunnel target), the region (which the bastion client must be
// pinned to), and the VCN that scopes bastion access. The region is derived
// from the cluster OCID's region segment (ocid1.cluster.oc1.<region>.<unique>)
// as a last-resort fallback — the authoritative region is the one OKE bakes
// into the kubeconfig exec args, which the caller prefers. An absent private
// endpoint is an error: a cluster without one is a public cluster the plugin
// cannot tunnel to. An absent VcnId is likewise an error: it is the only thing
// that scopes bastion access (Bastions filters on TargetVcnId), so an empty VCN
// would match every bastion and auto-select arbitrarily.
func ClusterFacts(ctx context.Context, c Client, clusterOCID string) (Facts, error) {
	resp, err := c.GetCluster(ctx, containerengine.GetClusterRequest{ClusterId: &clusterOCID})
	if err != nil {
		return Facts{}, fmt.Errorf("getting cluster %s: %w", clusterOCID, err)
	}
	if resp.Endpoints == nil || deref(resp.Endpoints.PrivateEndpoint) == "" {
		return Facts{}, fmt.Errorf("cluster %s has no private endpoint; only private OKE clusters can be tunneled", clusterOCID)
	}
	if deref(resp.VcnId) == "" {
		return Facts{}, fmt.Errorf("cluster %s reports no VCN; cannot determine which bastions can reach it", clusterOCID)
	}
	region, err := regionFromOCID(clusterOCID)
	if err != nil {
		return Facts{}, err
	}
	return Facts{
		PrivateEndpoint: deref(resp.Endpoints.PrivateEndpoint),
		Region:          region,
		VcnID:           deref(resp.VcnId),
	}, nil
}

// Bastions finds the bastions with access to the chosen cluster across every
// accessible compartment and reports whether exactly one was found (so the
// caller auto-selects rather than prompts). It searches the whole compartment
// list — the same one used for the cluster walk — because a bastion is commonly
// created in a separate hub/network compartment while still targeting the
// cluster's VCN; the compartment is incidental, the VCN match is the real
// precondition. "Access" is therefore filtered to ACTIVE bastions whose
// TargetVcnId is the cluster's VCN (a bastion forwards only into the VCN it was
// created against). vcnID must be non-empty (ClusterFacts guarantees this),
// else the filter would match every bastion. An empty result is an error: there
// is nothing to tunnel through.
//
// Like the cluster walk, the per-compartment listings fan out concurrently with
// aggregate-and-report semantics: every compartment is queried even after one
// fails, and the first failure is surfaced (wrapped, naming the compartment)
// rather than swallowed.
func Bastions(ctx context.Context, c Client, compartments []Compartment, vcnID string) (BastionResult, error) {
	if vcnID == "" {
		return BastionResult{}, fmt.Errorf("cannot list bastions: cluster VCN is empty")
	}

	type result struct {
		bastions []Bastion
		err      error
	}
	results := make([]result, len(compartments))

	var wg sync.WaitGroup
	for i, comp := range compartments {
		wg.Add(1)
		go func(i int, comp Compartment) {
			defer wg.Done()
			bastions, err := listBastionsForVcn(ctx, c, comp.OCID, vcnID)
			results[i] = result{bastions: bastions, err: err}
		}(i, comp)
	}
	wg.Wait()

	var (
		all      []Bastion
		firstErr error
	)
	for _, r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		all = append(all, r.bastions...)
	}
	if firstErr != nil {
		return BastionResult{}, firstErr
	}
	if len(all) == 0 {
		return BastionResult{}, fmt.Errorf("no active bastion targets the cluster's VCN %s in any accessible compartment", vcnID)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return BastionResult{Bastions: all, AutoSelected: len(all) == 1}, nil
}

// listBastionsForVcn lists one compartment's ACTIVE bastions that target vcnID,
// paging through OpcNextPage so a bastion past the first page is not dropped.
// The server-side state filter is belt-and-suspenders; the client-side state
// and VCN checks are what actually scope the result to reachable bastions.
func listBastionsForVcn(ctx context.Context, c Client, compartmentOCID, vcnID string) ([]Bastion, error) {
	var (
		out  []Bastion
		page *string
	)
	for {
		resp, err := c.ListBastions(ctx, ocibastion.ListBastionsRequest{
			CompartmentId:         &compartmentOCID,
			BastionLifecycleState: ocibastion.ListBastionsBastionLifecycleStateActive,
			Page:                  page,
		})
		if err != nil {
			return nil, fmt.Errorf("listing bastions in compartment %s: %w", compartmentOCID, err)
		}
		for _, b := range resp.Items {
			if b.LifecycleState != ocibastion.BastionLifecycleStateActive {
				continue
			}
			if deref(b.TargetVcnId) != vcnID {
				continue
			}
			out = append(out, Bastion{OCID: deref(b.Id), Name: deref(b.Name)})
		}
		if resp.OpcNextPage == nil {
			break
		}
		page = resp.OpcNextPage
	}
	return out, nil
}

// Kubeconfig renders the chosen cluster's kubeconfig YAML via OKE
// CreateKubeconfig, asking for the v2.0.0 exec-token form (the one the rest of
// the plugin parses) and the private endpoint (the only endpoint a tunneled
// cluster exposes). The response body is a stream; it is read fully and the
// bytes returned for the caller to merge.
func Kubeconfig(ctx context.Context, c Client, clusterOCID string) ([]byte, error) {
	tokenVersion := "2.0.0"
	resp, err := c.CreateKubeconfig(ctx, containerengine.CreateKubeconfigRequest{
		ClusterId: &clusterOCID,
		CreateClusterKubeconfigContentDetails: containerengine.CreateClusterKubeconfigContentDetails{
			TokenVersion: &tokenVersion,
			Endpoint:     containerengine.CreateClusterKubeconfigContentDetailsEndpointPrivateEndpoint,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating kubeconfig for cluster %s: %w", clusterOCID, err)
	}
	if resp.Content == nil {
		return nil, fmt.Errorf("creating kubeconfig for cluster %s: response carried no content", clusterOCID)
	}
	defer func() { _ = resp.Content.Close() }()
	raw, err := io.ReadAll(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig for cluster %s: %w", clusterOCID, err)
	}
	return raw, nil
}

// regionFromOCID extracts the region segment from an OCID of the form
// ocid1.<type>.<realm>.<region>.<unique>. OKE cluster OCIDs always carry the
// region in the fourth dot-separated field; a malformed OCID is an error rather
// than a silently wrong region.
func regionFromOCID(ocid string) (string, error) {
	parts := strings.Split(ocid, ".")
	if len(parts) < 5 || parts[3] == "" {
		return "", fmt.Errorf("cannot derive region from cluster OCID %q", ocid)
	}
	return parts[3], nil
}

// deref returns the pointed-to string, or "" for a nil pointer. The OCI SDK
// returns *string everywhere; this keeps the call sites free of nil guards.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
