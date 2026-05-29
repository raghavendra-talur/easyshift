# Testing

## Make targets

| Target | Runs |
| --- | --- |
| `make` / `make build` | `go vet`, gofmt check, then build `./easyshift` |
| `make test` | gofmt check + vet, then `go test ./...` |
| `make check` | `lint` + `build` + `test` — the pre-push gate |
| `make lint.go.light` | `go vet` + gofmt check |
| `make lint.go.full` | light lint + `golangci-lint` (pinned, via `go run`) |
| `make lint.make` | `checkmake` against the Makefile |
| `make fix.go.fmt` | apply `go fmt ./...` |

`golangci-lint` (v1.59.1) and `checkmake` (v0.3.2) are invoked via
`go run ...@version`, so a system install is neither needed nor used. The
Makefile is the source of truth — a bare `go build`/`go test` skips the
formatting and vet gates that `make` enforces.

Run **`make check`** before every push.

## How the code is testable

The whole design exists to make stages testable without real infrastructure:

- Stages depend only on **interfaces**, injected via their `New(...)`
  constructor.
- `providers/fakes` provides in-memory doubles for every interface.
- `fakes.All()` returns a wired `Deps` + a `Bundle`, so a test can build a real
  `app.ClusterManager` over fakes and exercise the actual pipeline.

## Unit test patterns

### Pipeline tests (`app/cluster_test.go`)

Build a manager over fakes, run a real lifecycle operation, and assert on what
the fakes recorded:

```go
cfg, deps, bundle := newTestEnv(t)
mgr := app.NewClusterManager(cfg, deps)

if err := mgr.Create(context.Background(), newNATCluster("hub")); err != nil {
    t.Fatalf("create hub: %v", err)
}

// Assert on recorded intent, not on real side effects.
if bundle.Net.Ensured[0].Name != config.SharedNATNetwork {
    t.Errorf(...)
}
```

These cover the high-value behaviors: happy path, resume after failure,
idempotent re-create, rollback on delete, NAT magic-DNS defaulting, the
shared-NAT multi-cluster property, and preflight rejections.

### Provider tests (`providers/libvirt/*_test.go`)

Drive a provider with a fake `CommandRunner` and assert on the commands it would
run. The `RunFunc` hook lets a test return errors for specific subcommands to
exercise cleanup paths:

```go
cmd := &fakes.CommandRunner{
    RunFunc: func(name string, args []string) ([]byte, error) {
        if contains(args, "net-start") { return nil, errors.New("boom") }
        return nil, nil
    },
}
// ... assert EnsureNetwork undefines the network after a failed net-start.
```

Internal-detail tests (e.g. XML builders) live in the package itself
(`package libvirt`) as `*_internal_test.go`; black-box tests use
`package libvirt_test`.

## What to test when you add code

- **A new stage** → a pipeline-level test that it does the right thing on
  `Apply`, undoes it on `Rollback`, and (if it has one) that `Preflight` fails
  with a useful error. Use fakes; assert on recorded calls.
- **A new provider** → table/command-shape tests with a fake `CommandRunner` or
  the relevant fake, including at least one failure path.
- **A new fake** → keep its recording shape parallel to the real calls so
  assertions read naturally.

## `--simulate` as a manual harness

`easyshift <cmd> --simulate` runs the real pipeline against fakes in a throwaway
config dir and prints `Bundle.WriteTrace` output — every operation a real run
would perform. Useful for eyeballing wiring changes without standing up libvirt.
