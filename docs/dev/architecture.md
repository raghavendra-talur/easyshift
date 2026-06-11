# Architecture

easyshift is a small Go module (`github.com/TheEasyShift/easyshift`)
organized so you can read it top-down: the CLI wires up an assembler, the
assembler runs an ordered list of stages, and each stage does one idempotent
piece of work through narrow interfaces.

## Package layering

There are six top-level packages, with a strict acyclic dependency flow:

```
config  ←  interfaces  ←  ┬─ stages/*      ┐
                          └─ providers/*   ┘  ←  app  ←  cmd
```

| Package | Responsibility | May import |
| --- | --- | --- |
| `config` | Plain data: `Config`, `ClusterConfig`, constants, path helpers. No behavior, no other internal package. | (stdlib only) |
| `interfaces` | Every behavioral interface + the spec/DTO types they exchange (`Deps`, `StageContext`, `Stage`, `NetworkSpec`, …). | `config` |
| `stages/*` | One package per install step. Each holds the interfaces it needs as struct fields, injected at construction. | `config`, `interfaces` |
| `providers/*` | Concrete implementations of the interfaces (libvirt, openshift, dns, tls, host, csr, exec, fileserver, fakes). Independent of each other. | `config`, `interfaces` |
| `app` | The assembler: builds `Deps`, owns the lifecycle `ClusterManager` and the stage `Runner`. **The only package that imports concrete providers and stage packages.** | all of the above |
| `cmd/easyshift` | cobra CLI; flag parsing; chooses production vs `--simulate` wiring. | `app`, `config`, `interfaces`, `providers/fakes` |

The rules that keep this honest:

- **Stages never import each other**, and never import `providers`. They depend
  only on `interfaces`. This is what lets a stage be unit-tested with fakes and
  keeps the graph acyclic.
- **Providers never import each other.** Each is a self-contained adapter.
- **`app` is the seam.** It's the single place that knows both the concrete
  providers and the concrete stages, and maps one onto the other.

## The staged-installer model

A cluster install is an ordered list of **stages** (`app/manager.go:buildStages`).
The `Runner` (`app/runner.go`) drives them and persists progress in
`clusters/<name>/state.json`.

Each `Stage` (`interfaces/stage.go`) is:

```go
type Stage interface {
    Name() string
    Apply(ctx, sc *StageContext) error      // idempotent; tolerates retry
    Rollback(ctx, sc *StageContext) error    // undoes a successful Apply
}
```

and may optionally implement `Preflighter` to declare checks that must pass
before *any* stage applies (the runner aggregates all preflight failures so you
see every problem at once).

### Apply / resume / rollback

- **Apply** runs stages in order; after each success the runner records a
  `StageRecord` in `state.json`.
- **Resume**: re-running `create` for a non-running cluster skips stages already
  marked applied and continues from the first unfinished one. This is why
  `Apply` must be idempotent.
- **Rollback** (used by `delete`) walks applied stages in reverse, calling
  `Rollback` on each, then removes the cluster directory.

The current pipeline, in order:

```
registercluster → allocatenetwork → upsertdns → ensureclusterdir →
downloadbinaries → downloadrhcos → generatesshkey → generateignition →
embedignitioniso → createlibvirtnetwork → createmastervms →
waitforinstall → applytlscerts → finalize
```

See **[stages.md](stages.md)** for the contract and how to add one.

## Dependency injection: `StageContext` and `Deps`

Two types carry state into stages, and they're deliberately different:

- **`StageContext`** (`interfaces/stage.go`) — the *data* a stage acts on:
  `Cluster` and `Config`, plus pure path/spec helpers derived from them
  (`ClusterDir()`, `KubeconfigPath()`, `InstallerSpec()`, …). It carries **no
  behavior**. Stages treat `Cluster`/`Config` as mutable; the runner persists
  the changes.
- **`Deps`** (`interfaces/deps.go`) — the *behavior* bag: one field per
  side-effect interface (`VM`, `Net`, `Installer`, `DNS`, `DNSManager`, `Host`,
  `Cmd`, …). `app` populates it (production via `app/deps.go:NewProductionDeps`,
  tests via `providers/fakes`), and `buildStages` maps each field into the
  constructor of the stage that needs it. **Stages never receive `Deps`** — they
  get exactly the interfaces they depend on, so a stage's signature documents
  its dependencies.

`NewCertIssuer` is a function field rather than a plain interface because per-
cluster ACME settings (email, staging) aren't known until a specific cluster is
being created.

## `--simulate`

`cmd/easyshift` can swap the production `Deps` for `providers/fakes` against a
throwaway config dir. The full pipeline runs against in-memory fakes and prints
a trace of every operation. This is both a user-facing dry run and the backbone
of the unit tests — see **[testing.md](testing.md)**.

## Lifecycle entry points (`app/manager.go`)

`ClusterManager` does validation, defaulting, and stage-list assembly only; all
side effects are delegated to stages.

- `Create` — validate name → resolve channel alias (e.g. `stable`) to a concrete
  version → preflight → apply (resuming if the cluster already exists).
- `Start` / `Stop` — boot/shutdown all VM names for the cluster.
- `Delete` — stop if running → rollback all stages → remove the cluster dir.
- `Status` (`app/status.go`) — read-only diagnostics, returns a `StatusReport`.

Defaulting (`applyDefaults`) and validation (`validateNew`) encode the product
rules: NAT vs bridge required flags, magic-DNS resolution, SNO-only, max 3
clusters, and the mutual exclusions between `--magic-dns`, `--dns-provider`, and
`--base-domain`.
