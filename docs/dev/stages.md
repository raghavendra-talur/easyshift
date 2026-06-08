# Stages

A stage is one idempotent step in the cluster lifecycle. The install pipeline is
just an ordered list of them (`app/manager.go:buildStages`), driven by the
`Runner` and tracked in `clusters/<name>/state.json`.

## The contract

From `interfaces/stage.go`:

```go
type Stage interface {
    Name() string                              // stable key used in state.json
    Apply(ctx context.Context, sc *StageContext) error
    Rollback(ctx context.Context, sc *StageContext) error
}

// Optional:
type Preflighter interface {
    Preflight(ctx context.Context, sc *StageContext) error
}
```

Rules every stage must honor:

- **`Apply` is idempotent.** It runs on first install *and* on resume after a
  partial failure, so it must tolerate "already done" — check-then-act, ignore
  "already exists" errors, etc. Don't assume a clean slate.
- **`Rollback` undoes a successful `Apply`** and is also best-effort idempotent
  (it may run against partially-removed state). Ignore "not found"-style errors.
- **`Name()` is a stable identifier.** It's the key in `state.json`; renaming it
  orphans existing state.
- **`Preflight` has no side effects.** It only validates. The runner calls
  `Preflight` on every stage and aggregates *all* failures before any `Apply`
  runs, so users see every problem at once.

## Anatomy of a stage package

One package per stage under `stages/`. A stage holds the interfaces it needs as
struct fields, injected by its `New(...)` constructor. It imports **only**
`config` and `interfaces` — never `providers`, never another stage.

```go
// Package createlibvirtnetwork ensures the shared NAT network exists and
// adds this cluster's DHCP reservation.
package createlibvirtnetwork

type Stage struct {
    net interfaces.NetworkProvisioner
    vm  interfaces.VMManager
}

func New(net interfaces.NetworkProvisioner, vm interfaces.VMManager) *Stage {
    return &Stage{net: net, vm: vm}
}

func (*Stage) Name() string { return "create-libvirt-network" }

func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error { ... }
func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error { ... }
```

`StageContext` gives you the cluster + config and pure derived helpers
(`sc.ClusterDir()`, `sc.KubeconfigPath()`, `sc.InstallerSpec()`, …). Stages may
mutate `sc.Cluster`/`sc.Config`; the runner persists those changes.

## The current pipeline

In `buildStages` order:

| # | Stage | Does | Key deps |
| --- | --- | --- | --- |
| 1 | `registercluster` | Adds the cluster to `config.Clusters`. | — |
| 2 | `allocatenetwork` | Allocates MAC/IP from the shared subnet; derives the magic-DNS domain. | — |
| 3 | `upsertdns` | Creates `api`/`api-int`/`*.apps` records (if `--dns-provider`). | `DNSManager` |
| 4 | `ensureclusterdir` | Creates `clusters/<name>/`. | — |
| 5 | `downloadbinaries` | Fetches `openshift-install` + `oc` for the version. | `Downloader`, `Cmd`, `Host` |
| 6 | `downloadrhcos` | Fetches/caches the RHCOS live ISO + `coreos-installer`. | `Installer`, `Downloader` |
| 7 | `generatesshkey` | Generates the cluster SSH keypair. | `Cmd`, `Host` |
| 8 | `generateignition` | Renders `install-config.yaml` and produces ignition. | `Installer`, `DNS`, `Host` |
| 9 | `embedignitioniso` | Builds the bootstrap-in-place boot ISO + uploads it to the pool. In bridge mode also embeds a NetworkManager keyfile that pins the master's static IP, so the node never depends on DHCP timing. | `Installer`, `VM` |
| 10 | `createlibvirtnetwork` | Ensures the shared NAT network + this cluster's DHCP reservation. | `Net`, `VM` |
| 11 | `createmastervms` | Creates the master VM(s) booting from the ISO. | `VM`, `Host` |
| 12 | `verifymasterip` | Bridge mode only: safety net that aborts fast if the booted node didn't come up on its IP (e.g. a LAN address conflict). The static keyfile from `embedignitioniso` is the primary defense; this catches the residual cases. No-op in NAT. | `Host` |
| 13 | `waitforinstall` | Waits for install-complete; approves CSRs; injects hostname. | `Installer`, `CSR`, `Hostname`, `VM` |
| 14 | `applytlscerts` | Issues + applies Let's Encrypt certs (if `--tls-email`), then rewrites the admin kubeconfig to trust the public cert. | `NewCertIssuer`, `Cmd` |
| 15 | `finalize` | Marks the cluster running. | — |

Preflight checks live on the stages that own the relevant precondition — e.g.
`createmastervms` preflights libvirt reachability, storage pool, CPU
virtualization, disk space, and (bridge mode) the host bridge.

## Adding a stage

1. Create `stages/<name>/stage.go` with a `New(...)` taking the interfaces it
   needs and a `Stage` implementing the contract above.
2. If it has preconditions, implement `Preflight` (no side effects).
3. If it needs a new capability, add the interface to `interfaces` and a
   concrete implementation under `providers/` (see [providers.md](providers.md)),
   plus a fake in `providers/fakes`.
4. Wire it into `app/manager.go:buildStages` at the right position, passing the
   matching `Deps` field(s).
5. Add unit tests (drive `Apply`/`Rollback`/`Preflight` with fakes) — see
   [testing.md](testing.md).

Order matters: a stage may depend on state produced by an earlier one (e.g.
`createmastervms` needs the ISO from `embedignitioniso` and the network from
`createlibvirtnetwork`). Place it after its producers.
