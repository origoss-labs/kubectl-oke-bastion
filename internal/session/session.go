// Package session owns the OCI Bastion port-forwarding session lifecycle:
// create it against the cluster's private endpoint, wait until it is ACTIVE,
// and delete it on exit. It speaks to OCI through the BastionClient interface
// so the lifecycle is exercised against a fake, never a live tenancy. No tunnel
// is opened here — that is slice 4's job; this slice proves a session can be
// brought up and torn down.
package session

import (
	"context"
	"fmt"
	"time"

	ocibastion "github.com/oracle/oci-go-sdk/v65/bastion"
)

// MaxTTLSeconds is OCI's hard cap on session lifetime (180 min). We always ask
// for the maximum and let the reactive supervisor (ADR-0006) recreate on
// expiry rather than rotating proactively.
const MaxTTLSeconds = 10800

// defaultPollInterval paces GetSession while a session is CREATING.
const defaultPollInterval = 2 * time.Second

// BastionClient is the slice of the OCI bastion client the lifecycle needs.
// *ocibastion.BastionClient satisfies it; tests supply a fake.
type BastionClient interface {
	CreateSession(context.Context, ocibastion.CreateSessionRequest) (ocibastion.CreateSessionResponse, error)
	GetSession(context.Context, ocibastion.GetSessionRequest) (ocibastion.GetSessionResponse, error)
	DeleteSession(context.Context, ocibastion.DeleteSessionRequest) (ocibastion.DeleteSessionResponse, error)
}

// Target is the private resource the session forwards to: the OKE API server's
// private endpoint.
type Target struct {
	PrivateIP string
	Port      int
}

// Params is everything Open needs to bring up a session.
type Params struct {
	BastionID        string
	Target           Target
	PublicKeyOpenSSH string
	// PollInterval overrides the GetSession poll cadence; zero uses the default.
	PollInterval time.Duration
}

// Session is a live, ACTIVE bastion session plus the SSH facts slice 4 dials
// with.
type Session struct {
	ID       string
	Username string
	SSHMeta  map[string]string

	client BastionClient
}

// Open creates the port-forwarding session and blocks until it is ACTIVE.
func Open(ctx context.Context, c BastionClient, p Params) (*Session, error) {
	bastionID := p.BastionID
	port := p.Target.Port
	ip := p.Target.PrivateIP
	pub := p.PublicKeyOpenSSH
	ttl := MaxTTLSeconds

	resp, err := c.CreateSession(ctx, ocibastion.CreateSessionRequest{
		CreateSessionDetails: ocibastion.CreateSessionDetails{
			BastionId:           &bastionID,
			SessionTtlInSeconds: &ttl,
			KeyDetails:          &ocibastion.PublicKeyDetails{PublicKeyContent: &pub},
			TargetResourceDetails: ocibastion.CreatePortForwardingSessionTargetResourceDetails{
				TargetResourcePrivateIpAddress: &ip,
				TargetResourcePort:             &port,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating bastion session: %w", err)
	}
	if resp.Id == nil {
		return nil, fmt.Errorf("creating bastion session: response carried no session id")
	}
	id := *resp.Id

	interval := p.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	s, err := waitActive(ctx, c, id, interval)
	if err != nil {
		// The session exists in OCI but never went ACTIVE; the caller has no
		// handle to delete it, so do it here on a fresh context (ctx may be the
		// reason we bailed). Best-effort: the activation error is what matters.
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = c.DeleteSession(delCtx, ocibastion.DeleteSessionRequest{SessionId: &id})
		return nil, err
	}
	return s, nil
}

// waitActive polls GetSession until the session reaches ACTIVE.
func waitActive(ctx context.Context, c BastionClient, id string, interval time.Duration) (*Session, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		got, err := c.GetSession(ctx, ocibastion.GetSessionRequest{SessionId: &id})
		if err != nil {
			return nil, fmt.Errorf("polling session %s: %w", id, err)
		}
		switch got.LifecycleState {
		case ocibastion.SessionLifecycleStateActive:
			s := &Session{ID: id, SSHMeta: got.SshMetadata, client: c}
			if got.BastionUserName != nil {
				s.Username = *got.BastionUserName
			}
			return s, nil
		case ocibastion.SessionLifecycleStateCreating:
			// Still coming up; keep polling.
		case ocibastion.SessionLifecycleStateFailed,
			ocibastion.SessionLifecycleStateDeleting,
			ocibastion.SessionLifecycleStateDeleted:
			return nil, fmt.Errorf("session %s entered terminal state %s before becoming active", id, got.LifecycleState)
		default:
			return nil, fmt.Errorf("session %s reported unknown lifecycle state %q", id, got.LifecycleState)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close deletes the session.
func (s *Session) Close(ctx context.Context) error {
	if _, err := s.client.DeleteSession(ctx, ocibastion.DeleteSessionRequest{SessionId: &s.ID}); err != nil {
		return fmt.Errorf("deleting session %s: %w", s.ID, err)
	}
	return nil
}
