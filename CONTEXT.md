# Context — kubectl-oke-bastion

Glossary of the domain language used in this project. Definitions only — no
implementation details. When a term here conflicts with how code or docs use a
word, this file wins; update it deliberately.

## Terms

**Plugin** — the `kubectl-oke-bastion` binary. Discovered by kubectl on `PATH`
and invoked as `kubectl oke bastion`.

**OKE** — Oracle Kubernetes Engine (OCI Container Engine for Kubernetes). The
managed Kubernetes service whose private API endpoint we reach.

**Private endpoint** — the OKE cluster's Kubernetes API server address, an IP on
a private subnet (`<ip>:6443`) that is not routable from the operator's machine.

**OCI Bastion** — an Oracle Cloud Infrastructure Bastion resource: a managed,
short-lived jump host into a private subnet. Pre-existing infrastructure from
this project's point of view; we do not create it.

**Session** — a single OCI Bastion *port-forwarding session*. Authorizes one SSH
port-forward to one target `IP:port` for a bounded time. Created and destroyed
by the plugin; the unit the plugin's lifecycle manages.

**Session TTL** — the lifetime a session is granted at creation. Hard-capped by
OCI at 180 minutes (3 hours). When it elapses the session is gone and a new one
must be created.

**Tunnel** — the live SSH local port-forward running through a session:
`localhost:<local-port>` → Bastion → `private-endpoint:6443`. A tunnel exists
only while both its session is valid and its SSH connection is up.

**Supervisor** — the foreground process that owns a tunnel for the duration of a
`kubectl oke bastion` invocation. Watches the tunnel and rebuilds it on failure.

**Break** — failure mode where the SSH connection drops while the session is
still valid. Recovered by redialing; no new session needed.

**Expiry** — failure mode where the session reaches its TTL and ends. Recovered
only by creating a new session, then a new tunnel.

**Bastion context** — the kubeconfig cluster+context entry the plugin adds
(suffix `-bastion`) pointing kubectl at the local end of the tunnel. Removed on
clean exit. The operator's original context is never mutated.

**Ephemeral key** — the throwaway SSH keypair the plugin generates per session to
authorize the port-forward. Never persisted beyond the session it serves.
