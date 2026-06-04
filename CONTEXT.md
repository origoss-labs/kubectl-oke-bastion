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

**Supervisor** — the process that owns a tunnel and rebuilds it on failure. Runs
as a detached **Daemon** (see below), not in the foreground.

**Daemon** — the background, detached process that runs the Supervisor for a
configured cluster, hidden from the operator. Started by `up`, stopped by
`down`, inspected by `status`. Persists across terminal sessions until stopped.

**init** — the onboarding command. Interactively selects an OCI Profile, walks
the tenancy's compartments to find OKE clusters, and selects a cluster and its
Bastion; generates and merges the cluster's kubeconfig; and writes the choices to
the Config. Does not start the Daemon.

**up / down / status** — the Daemon lifecycle commands: `up` starts the Daemon
for a configured cluster and returns immediately; `down` stops it; `status`
reports whether the tunnel is currently up.

**Profile** — a named credential section in the operator's `~/.oci/config`. init
records which Profile the Daemon authenticates with.

**Config** — the single YAML file holding the operator's choices: the Profile and
a list of configured clusters (each with its cluster, region, compartment,
Bastion, and kube context). The source of truth for `up`. Supersedes the earlier
cluster→bastion store.

**Break** — failure mode where the SSH connection drops while the session is
still valid. Recovered by redialing; no new session needed.

**Expiry** — failure mode where the session reaches its TTL and ends. Recovered
only by creating a new session, then a new tunnel.

**Bastion context** — the kubeconfig cluster+context entry the plugin adds
(suffix `-bastion`) pointing kubectl at the local end of the tunnel. Removed on
clean exit. The operator's original context is never mutated.

**Ephemeral key** — the throwaway SSH keypair the plugin generates per session to
authorize the port-forward. Never persisted beyond the session it serves.
