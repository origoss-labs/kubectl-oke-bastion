# OCI Go SDK — bastion package

Source: https://pkg.go.dev/github.com/oracle/oci-go-sdk/v65/bastion · fetched
2026-06-03. **Verify exact field names against the pinned SDK version when
coding** — signatures below are a guide, not a contract.

Import: `github.com/oracle/oci-go-sdk/v65/bastion` (+ `.../v65/common` for auth).

## Client

```go
NewBastionClientWithConfigurationProvider(cp common.ConfigurationProvider) (BastionClient, error)
NewBastionClientWithOboToken(cp common.ConfigurationProvider, oboToken string) (BastionClient, error)
```

`common.ConfigurationProvider` is where our `--auth` choice plugs in:
- api_key → `common.DefaultConfigProvider()` / `common.CustomProfileConfigProvider(path, profile)`
- security_token → session-token provider from `~/.oci/config` `security_token_file`
- instance_principal → `auth.InstancePrincipalConfigurationProvider()` (`.../v65/common/auth`)

## Session methods

```go
(c BastionClient) CreateSession(ctx, CreateSessionRequest)  (CreateSessionResponse, error)
(c BastionClient) GetSession(ctx, GetSessionRequest)        (GetSessionResponse, error)
(c BastionClient) DeleteSession(ctx, DeleteSessionRequest)  (DeleteSessionResponse, error)
(c BastionClient) ListSessions(ctx, ListSessionsRequest)    (ListSessionsResponse, error)
```

## Create-session shapes

```go
type CreateSessionDetails struct {
    BastionId             *string
    KeyDetails            *PublicKeyDetails               // PublicKeyContent *string
    SessionTtlInSeconds   *int                            // set to max (10800) per ADR-0006
    TargetResourceDetails CreateSessionTargetResourceDetails // interface
    // DisplayName, etc.
}

// Port-forwarding target (implements CreateSessionTargetResourceDetails)
type CreatePortForwardingSessionTargetResourceDetails struct {
    TargetResourcePort             *int     // 6443
    TargetResourcePrivateIpAddress *string  // OKE private endpoint IP
}
```

Sibling target types (not used here): `CreateManagedSshSessionTargetResourceDetails`,
`CreateDynamicPortForwardingSessionTargetResourceDetails`.

## Lifecycle state

```go
type SessionLifecycleStateEnum string
// values include: CREATING, ACTIVE, DELETING, DELETED, FAILED  (poll until ACTIVE)
GetSessionLifecycleStateEnumValues() []SessionLifecycleStateEnum
```

The created session's response also carries SSH connection metadata (the
`host.bastion.<region>...` endpoint + the session-OCID username) used to build
the in-process forward.

## Also needed: OKE GetCluster

For the private endpoint IP / verifying the cluster, the containerengine package:
`github.com/oracle/oci-go-sdk/v65/containerengine` → `GetCluster` →
`Cluster.Endpoints.PrivateEndpoint`. In practice we read the endpoint, cluster
OCID, and region from the current kubeconfig context first (ADR's input model);
the SDK call is the fallback / validation path.
