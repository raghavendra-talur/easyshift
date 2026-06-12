# Local CA, kubeconfig merge, and start convergence

Date: 2026-06-12
Status: approved design, pending implementation plan

## Goal

Adopt three minikube-style usability tricks so a freshly created cluster is
usable the moment `create` returns, with no TLS warnings and no manual
credential hunting:

1. **Local CA (extended).** Every cluster that does not use Let's Encrypt gets
   `api.<fqdn>` and `*.apps.<fqdn>` serving certs signed by a single
   host-local "easyshift local CA". A one-time `easyshift trust` installs that
   CA into the host's trust stores so the web console loads without warnings.
2. **Kubeconfig merge.** `create` merges an admin context into
   `~/.kube/config` named after the cluster and makes it the current context;
   `delete` removes exactly what was added.
3. **CSR approval on start.** `easyshift start` waits for the API to come
   back and approves pending node CSRs until the node is Ready, fixing the
   stopped-cluster certificate-expiry trap.

## Decisions already made

- Trust-store installation is an **explicit, separate** `easyshift trust`
  command (create stays root-free; privilege escalation is opt-in and visible).
- Local-CA signing applies to **all clusters without `--tls-email`** (NAT and
  bridge alike). Let's Encrypt, when configured, takes precedence unchanged.
- Kubeconfig merge **sets current-context** (minikube behavior).
- `start` **blocks** until the node converges (API up, CSRs approved, node
  Ready) rather than returning early or requiring a separate repair command.

## Architecture fit

Follows the existing acyclic flow (`config ← interfaces ← stages/providers ←
app ← cmd`):

- New provider `providers/localca` implementing the existing
  `interfaces.CertIssuer`.
- New provider `providers/truststore` implementing a new
  `interfaces.TrustStoreInstaller`.
- Generalized stage `stages/applytlscerts` (issuer selection).
- New stage `stages/mergekubeconfig`, inserted between `apply-tls-certs` and
  `finalize` in `app/manager.go:buildStages`.
- Start convergence in `app` (a helper used by `ClusterManager.Start`) — not a
  stage, because start/stop are not staged.
- `cmd` gains the `trust` command and an end-of-create summary.
- `interfaces.Deps` gains `NewLocalCertIssuer` and `TrustStore` fields;
  `providers/fakes` gains fakes for both so `--simulate` and unit tests cover
  everything.

## 1. providers/localca

Implements `interfaces.CertIssuer` (`Issue(ctx, domains) (certPEM, keyPEM,
error)`) with pure `crypto/x509` — no new module dependencies.

- **CA material**: `<configdir>/ca/ca.crt` + `ca.key` (dir `0700`, files
  `0600`). Generated lazily the first time `Issue` runs (or by `easyshift
  trust`, whichever comes first). ECDSA P-256, CN `easyshift local CA`,
  10-year validity, `IsCA`, key usage CertSign|CRLSign. One CA is shared by
  all clusters forever (like the ACME account dir).
- **Leaf certs**: ECDSA P-256, 825-day validity (Apple's trust cap for
  private-CA-issued TLS certs — relevant to the mac+linux vision), DNS SANs
  exactly as passed (wildcards supported), ext key usage ServerAuth.
- **Returned PEM**: cert bytes are leaf followed by the CA cert (full chain
  for the serving secret); key is PKCS#8.
- New path helper `config.LocalCADir(configDir)` alongside the existing ACME
  helpers in `config/paths.go`.

## 2. stages/applytlscerts generalization

Constructor becomes `New(newCertIssuer, newLocalIssuer, cmd)`;
`interfaces.Deps` gains:

```go
NewLocalCertIssuer func(caDir string) (CertIssuer, error)
```

`Apply` selects the issuer:

| Cluster setting        | Issuer                       | Kubeconfig fix-up                          |
| ---------------------- | ---------------------------- | ------------------------------------------ |
| `TLSEmail` set         | ACME (unchanged, byte-for-byte today's path) | strip embedded CA → system trust (existing `makeKubeconfigPublic`) |
| `TLSEmail` empty       | local CA                     | **append** easyshift CA to the embedded CA bundle |

The stage is **no longer a no-op** when `TLSEmail` is unset. Both paths share
the existing machinery: issue `api.<fqdn>` and `*.apps.<fqdn>` to
`<clusterdir>/tls/`, plant the two TLS secrets, patch `apiserver/cluster`
named certificates and `ingresscontroller/default`.

**CA bundle append (local path).** The admin kubeconfig's embedded internal
CA stops validating `api.<fqdn>` once the named certificate is served, so the
stage appends the easyshift CA to `certificate-authority-data`:

- Back up the original to `<kubeconfig>.internal-ca` first (existing pattern,
  only if the backup doesn't already exist).
- Read the current bundle via `oc config view --raw -o jsonpath=...`, decode,
  and skip if our CA cert (DER compare) is already present — this is the
  idempotency check for resumes.
- Write back via `oc config set clusters.<entry>.certificate-authority-data
  <base64>` (default `--set-raw-bytes=false` decodes the base64 into the
  field). All through `CommandRunner`.
- Appending (not replacing) keeps the internal CA valid, so the kubeconfig
  works both during and after the apiserver's gradual cert rollout.

`Preflight` is unchanged (DNS-provider check applies only to the ACME path;
the local path has no preflight requirements). `Rollback` stays a no-op: the
cluster teardown removes the in-cluster objects, the cert files live in the
cluster dir, and the CA itself is global and intentionally retained.

**Known caveat (pre-existing):** patching `apiserver/cluster` triggers a
kube-apiserver rollout; on SNO that means a few minutes of API blips. This is
already true for the Let's Encrypt path; it now applies to every cluster.

## 3. stages/mergekubeconfig (new)

Runs after `apply-tls-certs` (so the merged context inherits the corrected CA
trust) and before `finalize`. Holds `cmd interfaces.CommandRunner` only.

Target file: the first path in `$KUBECONFIG` if set, else `~/.kube/config`
(via `os.UserHomeDir`). The stage creates the parent directory (`0755`) if
missing and always passes the resolved path via `--kubeconfig`, using the
cluster's own `oc` (`sc.OCBinaryPath()`).

**Apply** (each step idempotent, satisfying the retry contract):

1. Extract from the admin kubeconfig via `oc config view --raw -o
   jsonpath=...`: server URL, `client-certificate-data`, `client-key-data`,
   and `certificate-authority-data` (may be absent after the LE strip).
2. Decode and write `client.crt` / `client.key` (and `ca-bundle.crt` if CA
   data present) under `<clusterdir>/auth/`, mode `0600`.
3. Against the target kubeconfig:
   - `oc config set-cluster easyshift-<name> --server=<url>` plus
     `--certificate-authority=<ca-bundle> --embed-certs` when CA data exists
     (local-CA clusters); no CA flags when absent (LE clusters → system trust).
   - `oc config set-credentials easyshift-<name>-admin
     --client-certificate=... --client-key=... --embed-certs`.
   - `oc config set-context <name> --cluster=easyshift-<name>
     --user=easyshift-<name>-admin`.
   - `oc config use-context <name>`.

Naming: the context is plainly `<name>` (minikube-style ergonomics); cluster
and user entries carry the `easyshift-` prefix to avoid colliding with the
user's real entries. A pre-existing foreign context with the same name is
overwritten — documented behavior, same trade-off minikube makes.

**Rollback**: `oc config delete-context <name>`, `delete-cluster
easyshift-<name>`, `delete-user easyshift-<name>-admin` (each tolerating
"not found"), and `oc config unset current-context` only if it currently
equals `<name>`. This gives `delete` clean removal for free.

## 4. End-of-create summary and `easyshift trust`

**Summary** (printed by `cmd` after a successful `Create`):

- the context that is now current (`oc` / `kubectl` work immediately),
- the console URL `https://console-openshift-console.apps.<fqdn>`,
- the kubeadmin password **path** `<clusterdir>/auth/kubeadmin-password`
  (the secret itself is not printed),
- when the cluster used the local CA and `<configdir>/ca/trusted` does not
  exist: a one-line hint to run `easyshift trust` to remove browser warnings.

**`easyshift trust`** — new cobra command backed by:

```go
// interfaces
type TrustStoreInstaller interface {
    Install(ctx context.Context, caCertPath string) error
    Uninstall(ctx context.Context, caCertPath string) error
}
```

with `providers/truststore` as the real implementation and a fake in
`providers/fakes`. `Deps.TrustStore` carries it; it is consumed only by `cmd`
(like `PullSecret`), never by stages.

Behavior:

- Generate the CA first if missing (so `trust` works before the first
  cluster).
- **Linux system store** (sudo): if `/etc/pki/ca-trust/source/anchors/`
  exists → copy `easyshift-local-ca.crt` there and run `update-ca-trust
  extract` (Fedora/RHEL family); else if `/usr/local/share/ca-certificates/`
  exists → copy and run `update-ca-certificates` (Debian family); else fail
  with a clear message naming both expected locations. Probe paths are
  injectable for tests.
- **macOS system store** (sudo): `security add-trusted-cert -d -r trustRoot
  -k /Library/Keychains/System.keychain <ca>`. (The CLI may run on macOS
  hosts in the project's future; the provider supports darwin from day one.)
- **NSS databases** (no sudo): if `certutil` is on PATH, install with
  `certutil -A -t "C,," -n "easyshift local CA"` into `~/.pki/nssdb` (if
  present) and every Firefox profile directory containing `cert9.db`. If
  `certutil` is absent, print a note that Firefox/Chrome may need it.
- Write the `<configdir>/ca/trusted` marker on success (drives the
  end-of-create hint only; best-effort host-state signal).
- `easyshift trust --uninstall` reverses each step (remove anchor + re-run
  the update command, `certutil -D`, delete the marker), tolerating absence.

All execution goes through `CommandRunner`, so `--simulate` traces it and
tests assert exact invocations. `sudo` prompts on `/dev/tty`, so output
capture does not break password entry; the command's help text states sudo
will be used.

## 5. Start convergence

`ClusterManager.Start` (app/manager.go:138) keeps its current VM-boot loop,
then calls a new `app/converge.go` helper before saving state:

1. **API wait**: poll `oc --kubeconfig <admin> get --raw /readyz` every 10s
   until success; timeout 15 minutes (SNO cold boot is slow).
2. **Converge**: start the existing `Deps.CSR` approver
   (`providers/csr.Run`) in a goroutine with a cancelable context — the
   `waitforinstall` pattern — while polling every 10s until both: no CSRs in
   `Pending` state and the node's `Ready` condition is `True` (both checked
   via `oc ... -o jsonpath` through `Cmd`). Timeout 10 minutes after API-up.
3. Cancel the approver, log a ready message. On timeout, return an error that
   names the stuck condition (API never up / CSRs still pending / node not
   Ready); the VMs stay running and the cluster state is still set to
   running, since a slow-but-recovering cluster shouldn't be marked broken.

Uses only `Deps.Cmd` and `Deps.CSR`; with fakes everything resolves on the
first poll, so `--simulate` and unit tests are instant.

## 6. Testing

- `providers/localca`: issue against a fresh dir, parse with `crypto/x509`,
  verify chain, SANs, validity windows, CA reuse across calls, file modes.
- `stages/applytlscerts`: issuer selection per `TLSEmail`; local path plants
  secrets/patches identically to LE; bundle-append idempotency (second Apply
  is a no-op on the kubeconfig); `.internal-ca` backup written once.
- `stages/mergekubeconfig`: fake runner asserts the exact `oc config`
  sequence for both the with-CA and without-CA shapes; rollback removes the
  three entries and only conditionally unsets current-context.
- `providers/truststore`: per-family command sequences via injected probe
  paths; certutil present/absent branches; uninstall.
- `app`: start convergence happy path, API timeout, CSR-pending timeout.
- `--simulate` exercises the full wiring (new Deps fields populated by fakes).
- `make check` must pass.

## 7. Documentation

- New/extended `docs/user/` page covering: default local-CA behavior,
  `easyshift trust` / `--uninstall` (including the sudo requirement and NSS
  note), kubeconfig context naming and the overwrite caveat, the
  end-of-create summary, and start-convergence behavior.
- `docs/dev/stages.md`: the generalized `apply-tls-certs` and new
  `merge-kubeconfig` stages.
- `docs/dev/architecture.md` (or `Deps` docs): the two new Deps fields.

## Out of scope

- Leaf-cert renewal/rotation for clusters older than 825 days, and CA
  rotation/revocation.
- Windows trust stores.
- A shared client-certificate CA across clusters (the APIServer `clientCA`
  trick) — per-cluster `system:admin` kubeconfigs already cover admin access.
- Backfilling local-CA certs onto clusters created before this change.
