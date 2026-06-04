package session

import (
	"context"
	"testing"
	"time"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// fakeClient stands in for the OCI BastionClient. It records the create
// request, replays a scripted sequence of lifecycle states to successive
// GetSession calls, and records the deleted session id — no live OCI call.
type fakeClient struct {
	created ocibastion.CreateSessionRequest
	states  []ocibastion.SessionLifecycleStateEnum
	getN    int
	deleted string

	createErr, getErr, deleteErr error
}

const fakeSessionID = "ocid1.bastionsession.oc1..test"

func (f *fakeClient) CreateSession(_ context.Context, req ocibastion.CreateSessionRequest) (ocibastion.CreateSessionResponse, error) {
	f.created = req
	if f.createErr != nil {
		return ocibastion.CreateSessionResponse{}, f.createErr
	}
	id := fakeSessionID
	creating := ocibastion.SessionLifecycleStateCreating
	return ocibastion.CreateSessionResponse{Session: ocibastion.Session{Id: &id, LifecycleState: creating}}, nil
}

func (f *fakeClient) GetSession(_ context.Context, req ocibastion.GetSessionRequest) (ocibastion.GetSessionResponse, error) {
	if f.getErr != nil {
		return ocibastion.GetSessionResponse{}, f.getErr
	}
	state := f.states[len(f.states)-1]
	if f.getN < len(f.states) {
		state = f.states[f.getN]
	}
	f.getN++
	user := "ocid1.bastionsession.oc1..test"
	return ocibastion.GetSessionResponse{Session: ocibastion.Session{
		Id:              req.SessionId,
		LifecycleState:  state,
		BastionUserName: &user,
		SshMetadata:     map[string]string{"command": "ssh ..."},
	}}, nil
}

func (f *fakeClient) DeleteSession(_ context.Context, req ocibastion.DeleteSessionRequest) (ocibastion.DeleteSessionResponse, error) {
	if f.deleteErr != nil {
		return ocibastion.DeleteSessionResponse{}, f.deleteErr
	}
	f.deleted = *req.SessionId
	return ocibastion.DeleteSessionResponse{}, nil
}

func testParams() Params {
	return Params{
		BastionID:        "ocid1.bastion.oc1..b",
		Target:           Target{PrivateIP: "10.0.0.6", Port: 6443},
		PublicKeyOpenSSH: "ssh-rsa AAAAB3Nz test",
		PollInterval:     time.Millisecond,
	}
}

func TestOpen_CreatesPortForwardingSessionAtMaxTTL(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateActive}}

	if _, err := Open(context.Background(), fake, testParams()); err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	d := fake.created.CreateSessionDetails
	if d.BastionId == nil || *d.BastionId != "ocid1.bastion.oc1..b" {
		t.Errorf("BastionId = %v, want ocid1.bastion.oc1..b", d.BastionId)
	}
	if d.SessionTtlInSeconds == nil || *d.SessionTtlInSeconds != MaxTTLSeconds {
		t.Errorf("SessionTtlInSeconds = %v, want %d", d.SessionTtlInSeconds, MaxTTLSeconds)
	}
	if d.KeyDetails == nil || d.KeyDetails.PublicKeyContent == nil || *d.KeyDetails.PublicKeyContent != "ssh-rsa AAAAB3Nz test" {
		t.Errorf("PublicKeyContent = %v, want the supplied public key", d.KeyDetails)
	}
	target, ok := d.TargetResourceDetails.(ocibastion.CreatePortForwardingSessionTargetResourceDetails)
	if !ok {
		t.Fatalf("TargetResourceDetails is %T, want CreatePortForwardingSessionTargetResourceDetails", d.TargetResourceDetails)
	}
	if target.TargetResourcePrivateIpAddress == nil || *target.TargetResourcePrivateIpAddress != "10.0.0.6" {
		t.Errorf("target IP = %v, want 10.0.0.6", target.TargetResourcePrivateIpAddress)
	}
	if target.TargetResourcePort == nil || *target.TargetResourcePort != 6443 {
		t.Errorf("target port = %v, want 6443", target.TargetResourcePort)
	}
}

func TestOpen_ErrorsWhenSessionFails(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateFailed}}

	if _, err := Open(context.Background(), fake, testParams()); err == nil {
		t.Fatal("expected an error when the session reaches FAILED, got nil")
	}
}

// CreateSession leaves a real session behind in OCI. If it never reaches
// ACTIVE, Open's caller gets no handle to Close, so Open itself must delete it
// or the session lingers until its 3h TTL, burning quota.
func TestOpen_DeletesSessionThatNeverActivates(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateFailed}}

	if _, err := Open(context.Background(), fake, testParams()); err == nil {
		t.Fatal("expected an error, got nil")
	}
	if fake.deleted != fakeSessionID {
		t.Errorf("created session %q was not deleted after failing to activate (deleted=%q)", fakeSessionID, fake.deleted)
	}
}

// An unrecognized lifecycle state (e.g. a value OCI adds later) must fail fast
// rather than be treated as "still creating" and polled until the deadline.
func TestOpen_ErrorsOnUnknownState(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateEnum("WEIRD")}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := Open(ctx, fake, testParams()); err == nil {
		t.Fatal("expected an error on an unknown lifecycle state, got nil")
	}
	if fake.getN != 1 {
		t.Errorf("GetSession called %d times; an unknown state should error on the first read, not poll", fake.getN)
	}
}

func TestOpen_ErrorsWhenPollTimesOut(t *testing.T) {
	// Stuck in CREATING forever: Open must give up when the context expires
	// rather than poll indefinitely.
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateCreating}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := Open(ctx, fake, testParams()); err == nil {
		t.Fatal("expected an error when the poll context expires, got nil")
	}
}

func TestOpen_PollsUntilActive(t *testing.T) {
	// CREATING twice, then ACTIVE: Open must keep polling, not bail on the
	// first non-active read.
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{
		ocibastion.SessionLifecycleStateCreating,
		ocibastion.SessionLifecycleStateCreating,
		ocibastion.SessionLifecycleStateActive,
	}}

	s, err := Open(context.Background(), fake, testParams())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if fake.getN != 3 {
		t.Errorf("GetSession called %d times, want 3", fake.getN)
	}
	if s.ID != fakeSessionID {
		t.Errorf("session ID = %q, want %q", s.ID, fakeSessionID)
	}
}

func TestAlive_TrueWhileActive(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateActive}}
	s, err := Open(context.Background(), fake, testParams())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.Alive(context.Background()) {
		t.Error("Alive = false for an ACTIVE session, want true")
	}
}

func TestAlive_FalseWhenSessionGone(t *testing.T) {
	// ACTIVE for Open, then DELETED on the next read: a session that expired or
	// was torn down out from under us.
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{
		ocibastion.SessionLifecycleStateActive,
		ocibastion.SessionLifecycleStateDeleted,
	}}
	s, err := Open(context.Background(), fake, testParams())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Alive(context.Background()) {
		t.Error("Alive = true for a DELETED session, want false")
	}
}

// Deadline must be the session's created-at plus the 3h TTL, the boundary the
// supervisor's proactive rebuild watches.
func TestDeadline_IsCreatedAtPlusTTL(t *testing.T) {
	created := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	fake := &fakeClientWithCreated{
		fakeClient: fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateActive}},
		created:    created,
	}
	s, err := Open(context.Background(), fake, testParams())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := created.Add(MaxTTLSeconds * time.Second)
	if !s.Deadline().Equal(want) {
		t.Errorf("Deadline = %v, want created-at + TTL = %v", s.Deadline(), want)
	}
}

// fakeClientWithCreated extends fakeClient to stamp a TimeCreated on the ACTIVE
// session, so the deadline computation has a real created-at to read.
type fakeClientWithCreated struct {
	fakeClient
	created time.Time
}

func (f *fakeClientWithCreated) GetSession(ctx context.Context, req ocibastion.GetSessionRequest) (ocibastion.GetSessionResponse, error) {
	resp, err := f.fakeClient.GetSession(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Session.TimeCreated = &common.SDKTime{Time: f.created}
	return resp, nil
}

func TestClose_DeletesSession(t *testing.T) {
	fake := &fakeClient{states: []ocibastion.SessionLifecycleStateEnum{ocibastion.SessionLifecycleStateActive}}
	s, err := Open(context.Background(), fake, testParams())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if fake.deleted != fakeSessionID {
		t.Errorf("DeleteSession got id %q, want %q", fake.deleted, fakeSessionID)
	}
}
