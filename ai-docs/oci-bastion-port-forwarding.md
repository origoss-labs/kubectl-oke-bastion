# OCI Bastion port-forwarding → private OKE API endpoint

Sources (fetched 2026-06-03):
- https://docs.oracle.com/en-us/iaas/Content/Bastion/Tasks/create-session-port-forwarding.htm
- https://docs.oracle.com/en-us/iaas/Content/ContEng/Tasks/contengsettingupbastion.htm

This is the manual flow our plugin automates. **Note our deviation:** the OKE doc
edits `server:` to `127.0.0.1` in place; we instead add a separate `-bastion`
context with `tls-server-name` so TLS verifies (ADR-0005).

## Session inputs (port-forwarding)

| Input | Notes |
| --- | --- |
| Bastion OCID | `--bastion-id`. Bastion must pre-exist. |
| Target private IP | `--target-private-ip` = OKE cluster private endpoint IP |
| Target port | `--target-port 6443` for the K8s API |
| SSH public key | `--ssh-public-key-file`; private key used to connect |
| Session TTL | 30–180 min (max **180 / 3h**, hard cap). Optional. |

Port-forwarding sessions need **no** OpenSSH server or Oracle Cloud Agent on the
target (unlike managed-SSH sessions).

## Manual CLI flow (reference)

```bash
# 1. Generate the OKE kubeconfig (carries cluster-id + region in the exec creds,
#    and the private endpoint IP in server:)
oci ce cluster create-kubeconfig \
  --cluster-id <cluster OCID> \
  --file $HOME/.kube/config \
  --region <region> \
  --token-version 2.0.0

# 2. Create the port-forwarding session to the private API endpoint
oci bastion session create-port-forwarding \
  --bastion-id <bastion OCID> \
  --ssh-public-key-file <ssh public key> \
  --target-private-ip <API private IP endpoint> \
  --target-port 6443

# 3. Get the ready-made SSH command for the session
oci bastion session get --session-id <session OCID> \
  | jq '.data."ssh-metadata".command'
```

## SSH local-forward command (the form we replicate in-process)

```bash
ssh -i <privateKey> -N -L <localPort>:<session-IP>:<session-port> -p 22 <session-ocid>@host.bastion.<region>.oci.oraclecloud.com
```

Example:

```bash
ssh -i ~/.ssh/id_rsa -N -L 6443:10.0.0.6:6443 -p 22 ocid1.bastionsession...@host.bastion.<region>.oci.oraclecloud.com &
```

- SSH user = the **session OCID**; host = `host.bastion.<region>.oci.oraclecloud.com`; port 22.
- `-N` = no remote command, `-L localPort:targetIP:6443` = the local forward.
- We open this same forward via `golang.org/x/crypto/ssh` rather than shelling
  out (ADR-0002).

## Lifecycle facts that drive the supervisor

- A session is unusable until it reaches **ACTIVE**; poll `GetSession` after create.
- At TTL expiry the session ends — kubectl traffic stops; recovery requires a
  **new** session (ADR-0006 reactive recreate).
- Sessions can be deleted before expiry — we delete on clean exit (teardown).
- Known symptom: a port-forwarding session whose initial SSH connects but then
  times out after minutes usually means NSG/security-list rules block the path —
  a prerequisite/infra issue, not something the plugin retries away.
