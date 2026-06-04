package discover

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/containerengine"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// fakeOCI implements the narrow Client interface discover depends on. It is
// scripted per-compartment so tests can assert aggregation, ACTIVE filtering,
// labelling, auto-select, error propagation, and concurrency — never a live
// OCI call.
type fakeOCI struct {
	// compartments is the flat list ListCompartments paginates over (the root
	// compartment is supplied separately to Compartments via rootID/rootName).
	compartments []identity.Compartment
	listCmptErr  error

	// clustersByCompartment maps a compartment OCID to the clusters
	// ListClusters returns for it.
	clustersByCompartment map[string][]containerengine.ClusterSummary
	// listClustersErrFor names a compartment whose ListClusters fails.
	listClustersErrFor string

	cluster    containerengine.Cluster
	getCluster error

	// bastionsByCompartment maps a compartment OCID to the bastions ListBastions
	// returns for it; bastionPaginate, when set, splits each compartment's
	// bastions one-per-page to exercise the OpcNextPage loop.
	bastionsByCompartment map[string][]ocibastion.BastionSummary
	bastionPaginate       bool
	listBastEr            error
	listBastErrFor        string

	kubeconfig    []byte
	createKubeErr error

	// mu guards the call counters under the concurrent ListClusters and
	// ListBastions fan-outs.
	mu             sync.Mutex
	listClustersN  int
	listCmptPages  int
	bastPageByCmpt map[string]int
}

func (f *fakeOCI) ListCompartments(_ context.Context, req identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error) {
	if f.listCmptErr != nil {
		return identity.ListCompartmentsResponse{}, f.listCmptErr
	}
	f.mu.Lock()
	f.listCmptPages++
	page := f.listCmptPages
	f.mu.Unlock()
	// Page once: first call returns every compartment with a next-page token,
	// second call returns nothing — exercises the pagination loop.
	if page == 1 && len(f.compartments) > 0 {
		next := "page-2"
		return identity.ListCompartmentsResponse{Items: f.compartments, OpcNextPage: &next}, nil
	}
	return identity.ListCompartmentsResponse{}, nil
}

func (f *fakeOCI) ListClusters(_ context.Context, req containerengine.ListClustersRequest) (containerengine.ListClustersResponse, error) {
	f.mu.Lock()
	f.listClustersN++
	f.mu.Unlock()
	cmpt := *req.CompartmentId
	if cmpt == f.listClustersErrFor {
		return containerengine.ListClustersResponse{}, errors.New("boom: list clusters failed")
	}
	return containerengine.ListClustersResponse{Items: f.clustersByCompartment[cmpt]}, nil
}

func (f *fakeOCI) GetCluster(_ context.Context, req containerengine.GetClusterRequest) (containerengine.GetClusterResponse, error) {
	if f.getCluster != nil {
		return containerengine.GetClusterResponse{}, f.getCluster
	}
	return containerengine.GetClusterResponse{Cluster: f.cluster}, nil
}

func (f *fakeOCI) ListBastions(_ context.Context, req ocibastion.ListBastionsRequest) (ocibastion.ListBastionsResponse, error) {
	if f.listBastEr != nil {
		return ocibastion.ListBastionsResponse{}, f.listBastEr
	}
	cmpt := deref(req.CompartmentId)
	if cmpt == f.listBastErrFor {
		return ocibastion.ListBastionsResponse{}, errors.New("boom: list bastions failed")
	}
	items := f.bastionsByCompartment[cmpt]
	if !f.bastionPaginate {
		return ocibastion.ListBastionsResponse{Items: items}, nil
	}

	// Paginate one bastion per page within this compartment so the OpcNextPage
	// loop is exercised: track which page we are on per compartment.
	f.mu.Lock()
	if f.bastPageByCmpt == nil {
		f.bastPageByCmpt = map[string]int{}
	}
	idx := f.bastPageByCmpt[cmpt]
	f.bastPageByCmpt[cmpt]++
	f.mu.Unlock()
	if idx >= len(items) {
		return ocibastion.ListBastionsResponse{}, nil
	}
	resp := ocibastion.ListBastionsResponse{Items: items[idx : idx+1]}
	if idx+1 < len(items) {
		next := "next"
		resp.OpcNextPage = &next
	}
	return resp, nil
}

func (f *fakeOCI) CreateKubeconfig(_ context.Context, req containerengine.CreateKubeconfigRequest) (containerengine.CreateKubeconfigResponse, error) {
	if f.createKubeErr != nil {
		return containerengine.CreateKubeconfigResponse{}, f.createKubeErr
	}
	return containerengine.CreateKubeconfigResponse{Content: io.NopCloser(strings.NewReader(string(f.kubeconfig)))}, nil
}

func sp(s string) *string { return &s }

func cmpt(id, name string) identity.Compartment {
	return identity.Compartment{Id: sp(id), Name: sp(name)}
}

func activeCluster(id, name, cmptID string) containerengine.ClusterSummary {
	return containerengine.ClusterSummary{
		Id:             sp(id),
		Name:           sp(name),
		CompartmentId:  sp(cmptID),
		LifecycleState: containerengine.ClusterLifecycleStateActive,
	}
}

// --- Compartments ---

func TestCompartments_IncludesRootAndSubtreePaginated(t *testing.T) {
	f := &fakeOCI{compartments: []identity.Compartment{
		cmpt("ocid1.compartment.oc1..a", "team-a"),
		cmpt("ocid1.compartment.oc1..b", "team-b"),
	}}

	got, err := Compartments(context.Background(), f, "ocid1.tenancy.oc1..root", "root-tenancy")
	if err != nil {
		t.Fatalf("Compartments: %v", err)
	}

	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	want := []string{"root-tenancy", "team-a", "team-b"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("compartment names = %v, want %v (root must be included)", names, want)
	}
}

func TestCompartments_ListErrorSurfaced(t *testing.T) {
	f := &fakeOCI{listCmptErr: errors.New("denied")}
	if _, err := Compartments(context.Background(), f, "ocid1.tenancy.oc1..root", "root"); err == nil {
		t.Fatal("expected the ListCompartments error to be surfaced, got nil")
	}
}

// --- Clusters aggregation ---

func TestClusters_AggregatesActiveAcrossCompartmentsLabelled(t *testing.T) {
	f := &fakeOCI{
		clustersByCompartment: map[string][]containerengine.ClusterSummary{
			"ocid1.compartment.oc1..a": {
				activeCluster("ocid1.cluster.oc1..1", "prod", "ocid1.compartment.oc1..a"),
				// A non-ACTIVE cluster that must be filtered out.
				{Id: sp("ocid1.cluster.oc1..deleting"), Name: sp("old"), CompartmentId: sp("ocid1.compartment.oc1..a"), LifecycleState: containerengine.ClusterLifecycleStateDeleting},
			},
			"ocid1.compartment.oc1..b": {
				activeCluster("ocid1.cluster.oc1..2", "staging", "ocid1.compartment.oc1..b"),
			},
		},
	}
	compartments := []Compartment{
		{OCID: "ocid1.compartment.oc1..a", Name: "team-a"},
		{OCID: "ocid1.compartment.oc1..b", Name: "team-b"},
	}

	got, err := Clusters(context.Background(), f, compartments, nil)
	if err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("aggregated %d clusters, want 2 (only ACTIVE across both compartments)", len(got))
	}
	labels := map[string]bool{}
	for _, c := range got {
		labels[c.Label()] = true
	}
	if !labels["prod — team-a"] {
		t.Errorf("missing label %q in %v", "prod — team-a", labels)
	}
	if !labels["staging — team-b"] {
		t.Errorf("missing label %q in %v", "staging — team-b", labels)
	}
}

func TestClusters_ProgressReportedPerCompartment(t *testing.T) {
	f := &fakeOCI{
		clustersByCompartment: map[string][]containerengine.ClusterSummary{
			"ocid1.compartment.oc1..a": {activeCluster("ocid1.cluster.oc1..1", "c1", "ocid1.compartment.oc1..a")},
			"ocid1.compartment.oc1..b": {activeCluster("ocid1.cluster.oc1..2", "c2", "ocid1.compartment.oc1..b")},
			"ocid1.compartment.oc1..c": {},
		},
	}
	compartments := []Compartment{
		{OCID: "ocid1.compartment.oc1..a", Name: "a"},
		{OCID: "ocid1.compartment.oc1..b", Name: "b"},
		{OCID: "ocid1.compartment.oc1..c", Name: "c"},
	}

	var mu sync.Mutex
	var done, total int
	progress := func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		done = p.Done
		total = p.Total
	}

	if _, err := Clusters(context.Background(), f, compartments, progress); err != nil {
		t.Fatalf("Clusters: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if total != 3 {
		t.Errorf("final progress Total = %d, want 3", total)
	}
	if done != 3 {
		t.Errorf("final progress Done = %d, want 3 (all compartments completed)", done)
	}
	// Every compartment must have been queried exactly once by the fan-out.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listClustersN != 3 {
		t.Errorf("ListClusters called %d times, want 3 (one fan-out call per compartment)", f.listClustersN)
	}
}

func TestClusters_PerCompartmentErrorSurfacedNotSwallowed(t *testing.T) {
	f := &fakeOCI{
		clustersByCompartment: map[string][]containerengine.ClusterSummary{
			"ocid1.compartment.oc1..a": {activeCluster("ocid1.cluster.oc1..1", "c1", "ocid1.compartment.oc1..a")},
		},
		listClustersErrFor: "ocid1.compartment.oc1..b",
	}
	compartments := []Compartment{
		{OCID: "ocid1.compartment.oc1..a", Name: "a"},
		{OCID: "ocid1.compartment.oc1..b", Name: "b"},
	}

	_, err := Clusters(context.Background(), f, compartments, nil)
	if err == nil {
		t.Fatal("expected a per-compartment ListClusters error to be surfaced, got nil")
	}
	if !strings.Contains(err.Error(), "ocid1.compartment.oc1..b") {
		t.Errorf("error %q does not name the failing compartment", err)
	}
}

// --- Cluster facts ---

func TestClusterFacts_ReadsPrivateEndpointAndRegion(t *testing.T) {
	f := &fakeOCI{cluster: containerengine.Cluster{
		Id:    sp("ocid1.cluster.oc1.eu-frankfurt-1.aaaa"),
		Name:  sp("prod"),
		VcnId: sp("ocid1.vcn.oc1..v"),
		Endpoints: &containerengine.ClusterEndpoints{
			PrivateEndpoint: sp("10.0.0.6:6443"),
		},
	}}

	facts, err := ClusterFacts(context.Background(), f, "ocid1.cluster.oc1.eu-frankfurt-1.aaaa")
	if err != nil {
		t.Fatalf("ClusterFacts: %v", err)
	}
	if facts.PrivateEndpoint != "10.0.0.6:6443" {
		t.Errorf("PrivateEndpoint = %q, want 10.0.0.6:6443", facts.PrivateEndpoint)
	}
	// Region is derived from the cluster OCID's region segment.
	if facts.Region != "eu-frankfurt-1" {
		t.Errorf("Region = %q, want eu-frankfurt-1", facts.Region)
	}
	if facts.VcnID != "ocid1.vcn.oc1..v" {
		t.Errorf("VcnID = %q, want ocid1.vcn.oc1..v", facts.VcnID)
	}
}

func TestClusterFacts_NoPrivateEndpointErrors(t *testing.T) {
	f := &fakeOCI{cluster: containerengine.Cluster{
		Id:        sp("ocid1.cluster.oc1.eu-frankfurt-1.aaaa"),
		VcnId:     sp("ocid1.vcn.oc1..v"),
		Endpoints: &containerengine.ClusterEndpoints{},
	}}
	if _, err := ClusterFacts(context.Background(), f, "ocid1.cluster.oc1.eu-frankfurt-1.aaaa"); err == nil {
		t.Fatal("expected an error when the cluster has no private endpoint, got nil")
	}
}

// An empty VcnId would otherwise disable the bastion VCN filter and auto-select
// an arbitrary bastion, so ClusterFacts must reject it up front.
func TestClusterFacts_NoVcnErrors(t *testing.T) {
	f := &fakeOCI{cluster: containerengine.Cluster{
		Id:        sp("ocid1.cluster.oc1.eu-frankfurt-1.aaaa"),
		Endpoints: &containerengine.ClusterEndpoints{PrivateEndpoint: sp("10.0.0.6:6443")},
		// VcnId left nil.
	}}
	if _, err := ClusterFacts(context.Background(), f, "ocid1.cluster.oc1.eu-frankfurt-1.aaaa"); err == nil {
		t.Fatal("expected an error when the cluster has no VCN, got nil")
	}
}

// --- Bastions + auto-select ---

// twoCompartments is the accessible-compartment list passed to Bastions in the
// tests: the cluster's own compartment plus a separate hub/network compartment.
var twoCompartments = []Compartment{
	{OCID: "ocid1.compartment.oc1..cluster", Name: "cluster-cmpt"},
	{OCID: "ocid1.compartment.oc1..hub", Name: "hub-cmpt"},
}

func TestBastions_AutoSelectWhenExactlyOne(t *testing.T) {
	f := &fakeOCI{bastionsByCompartment: map[string][]ocibastion.BastionSummary{
		"ocid1.compartment.oc1..cluster": {{
			Id:             sp("ocid1.bastion.oc1..only"),
			Name:           sp("the-bastion"),
			TargetVcnId:    sp("ocid1.vcn.oc1..v"),
			LifecycleState: ocibastion.BastionLifecycleStateActive,
		}},
	}}

	res, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err != nil {
		t.Fatalf("Bastions: %v", err)
	}
	if len(res.Bastions) != 1 {
		t.Fatalf("got %d bastions, want 1", len(res.Bastions))
	}
	if !res.AutoSelected {
		t.Error("AutoSelected = false for a single bastion, want true")
	}
	if res.Bastions[0].OCID != "ocid1.bastion.oc1..only" {
		t.Errorf("bastion OCID = %q, want ocid1.bastion.oc1..only", res.Bastions[0].OCID)
	}
}

func TestBastions_FiltersToClusterVcnAndActive(t *testing.T) {
	f := &fakeOCI{bastionsByCompartment: map[string][]ocibastion.BastionSummary{
		"ocid1.compartment.oc1..cluster": {
			{Id: sp("ocid1.bastion.oc1..match"), Name: sp("match"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
			{Id: sp("ocid1.bastion.oc1..othervcn"), Name: sp("other"), TargetVcnId: sp("ocid1.vcn.oc1..OTHER"), LifecycleState: ocibastion.BastionLifecycleStateActive},
			{Id: sp("ocid1.bastion.oc1..deleting"), Name: sp("dead"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateDeleting},
		},
	}}

	res, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err != nil {
		t.Fatalf("Bastions: %v", err)
	}
	if len(res.Bastions) != 1 {
		t.Fatalf("got %d bastions, want only the ACTIVE one in the cluster's VCN", len(res.Bastions))
	}
	if res.Bastions[0].OCID != "ocid1.bastion.oc1..match" {
		t.Errorf("kept bastion %q, want the VCN-matching one", res.Bastions[0].OCID)
	}
	if !res.AutoSelected {
		t.Error("AutoSelected = false after filtering to one, want true")
	}
}

// A bastion can live in a hub/network compartment separate from the cluster's
// own compartment while still targeting the cluster's VCN. Bastions must search
// the whole accessible-compartment list and find it by VCN match, not by
// compartment.
func TestBastions_FindsBastionInDifferentCompartment(t *testing.T) {
	f := &fakeOCI{bastionsByCompartment: map[string][]ocibastion.BastionSummary{
		// Nothing in the cluster's own compartment.
		"ocid1.compartment.oc1..hub": {{
			Id:             sp("ocid1.bastion.oc1..hub"),
			Name:           sp("hub-bastion"),
			TargetVcnId:    sp("ocid1.vcn.oc1..v"),
			LifecycleState: ocibastion.BastionLifecycleStateActive,
		}},
	}}

	res, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err != nil {
		t.Fatalf("Bastions: %v", err)
	}
	if len(res.Bastions) != 1 || res.Bastions[0].OCID != "ocid1.bastion.oc1..hub" {
		t.Fatalf("got %+v, want the hub-compartment bastion found by VCN match", res.Bastions)
	}
	if !res.AutoSelected {
		t.Error("AutoSelected = false for the sole cross-compartment bastion, want true")
	}
}

// A bastion past the first page of ListBastions must not be silently dropped:
// the per-compartment listing pages through OpcNextPage like the cluster walk.
func TestBastions_PaginatesPastFirstPage(t *testing.T) {
	f := &fakeOCI{
		bastionPaginate: true,
		bastionsByCompartment: map[string][]ocibastion.BastionSummary{
			"ocid1.compartment.oc1..cluster": {
				{Id: sp("ocid1.bastion.oc1..page1"), Name: sp("page1"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
				{Id: sp("ocid1.bastion.oc1..page2"), Name: sp("page2"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
			},
		},
	}

	res, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err != nil {
		t.Fatalf("Bastions: %v", err)
	}
	if len(res.Bastions) != 2 {
		t.Fatalf("got %d bastions, want 2 (the second is on page 2 and must not be dropped)", len(res.Bastions))
	}
}

func TestBastions_MultipleNotAutoSelected(t *testing.T) {
	f := &fakeOCI{bastionsByCompartment: map[string][]ocibastion.BastionSummary{
		"ocid1.compartment.oc1..cluster": {
			{Id: sp("ocid1.bastion.oc1..one"), Name: sp("one"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
		},
		"ocid1.compartment.oc1..hub": {
			{Id: sp("ocid1.bastion.oc1..two"), Name: sp("two"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive},
		},
	}}

	res, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err != nil {
		t.Fatalf("Bastions: %v", err)
	}
	if len(res.Bastions) != 2 {
		t.Fatalf("got %d bastions, want 2 (one per compartment, both on the VCN)", len(res.Bastions))
	}
	if res.AutoSelected {
		t.Error("AutoSelected = true for two bastions, want false (caller must prompt)")
	}
}

func TestBastions_NoneErrors(t *testing.T) {
	f := &fakeOCI{bastionsByCompartment: nil}
	if _, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v"); err == nil {
		t.Fatal("expected an error when no bastion has access to the cluster, got nil")
	}
}

func TestBastions_ListErrorSurfaced(t *testing.T) {
	f := &fakeOCI{listBastEr: errors.New("denied")}
	if _, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v"); err == nil {
		t.Fatal("expected the ListBastions error to be surfaced, got nil")
	}
}

// A per-compartment ListBastions error must be surfaced (wrapped, naming the
// compartment), not swallowed — mirroring the cluster-walk policy.
func TestBastions_PerCompartmentErrorSurfaced(t *testing.T) {
	f := &fakeOCI{
		bastionsByCompartment: map[string][]ocibastion.BastionSummary{
			"ocid1.compartment.oc1..cluster": {{Id: sp("ocid1.bastion.oc1..ok"), Name: sp("ok"), TargetVcnId: sp("ocid1.vcn.oc1..v"), LifecycleState: ocibastion.BastionLifecycleStateActive}},
		},
		listBastErrFor: "ocid1.compartment.oc1..hub",
	}
	_, err := Bastions(context.Background(), f, twoCompartments, "ocid1.vcn.oc1..v")
	if err == nil {
		t.Fatal("expected a per-compartment ListBastions error to be surfaced, got nil")
	}
	if !strings.Contains(err.Error(), "ocid1.compartment.oc1..hub") {
		t.Errorf("error %q does not name the failing compartment", err)
	}
}

// --- CreateKubeconfig ---

func TestKubeconfig_ReturnsBytes(t *testing.T) {
	f := &fakeOCI{kubeconfig: []byte("apiVersion: v1\nkind: Config\n")}
	raw, err := Kubeconfig(context.Background(), f, "ocid1.cluster.oc1..1")
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	if !strings.Contains(string(raw), "kind: Config") {
		t.Errorf("kubeconfig bytes = %q, want the cluster's kubeconfig YAML", raw)
	}
}

func TestKubeconfig_ErrorSurfaced(t *testing.T) {
	f := &fakeOCI{createKubeErr: errors.New("denied")}
	if _, err := Kubeconfig(context.Background(), f, "ocid1.cluster.oc1..1"); err == nil {
		t.Fatal("expected the CreateKubeconfig error to be surfaced, got nil")
	}
}
