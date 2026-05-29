# Contributing to easyshift

Thanks for your interest in improving easyshift. This is the short version; the
full developer guide lives in **[docs/dev/](docs/dev/)**.

## Quick start

```sh
make check     # vet + gofmt check + build + unit tests — run this before every push
```

- `make build` — vet, gofmt check, then build `./easyshift`.
- `make test` — unit tests (`go test ./...`).
- `make lint.go.full` — adds `golangci-lint` (heavier).
- `make fix.go.fmt` — apply gofmt fixes.

`golangci-lint` and `checkmake` are pinned and run via `go run ...@version`, so
you don't need them installed.

## Where things live

- **[docs/dev/architecture.md](docs/dev/architecture.md)** — package layering and
  the staged-installer model.
- **[docs/dev/stages.md](docs/dev/stages.md)** — how to add or change a stage.
- **[docs/dev/providers.md](docs/dev/providers.md)** — interfaces, provider
  implementations, and fakes.
- **[docs/dev/testing.md](docs/dev/testing.md)** — testing conventions and
  `--simulate`.

## Commits and pull requests

- Sign off every commit: `git commit -s`.
- Keep commits focused; explain the *why* in the body.
- Run `make check` before pushing.

For the complete workflow — including this repo's commit-trailer convention —
see **[docs/dev/contributing.md](docs/dev/contributing.md)**.
