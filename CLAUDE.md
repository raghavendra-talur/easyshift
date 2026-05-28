# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`easyshift` is an opinionated OpenShift installer for developers. It is a Go CLI (cobra) that provisions OpenShift clusters as libvirt VMs on the local host. The binary refuses to run unless invoked as root (`cmd/easyshift/main.go:20`) because it shells out to `virsh` / `virt-install` and writes to system-level libvirt state.

## Commands

The Makefile is the source of truth — `go build` directly skips formatting checks the Makefile enforces.

- `make` / `make build` — vet, gofmt check, then build the `easyshift` binary at the repo root.
- `make test` — runs `go test ./...` (currently no `_test.go` files exist, so this is a no-op gate).
- `make check` — lint + build + test.
- `make lint.go.full` — runs `golangci-lint` (heavier than `lint.go.light`, which is just vet + fmt).
- `make lint.make` — runs `checkmake` against the Makefile (`maxBodyLength = 8` in `checkmake.ini`).
- `make fix.go.fmt` — apply `go fmt` to fix formatting.

`golangci-lint` and `checkmake` are pinned and invoked via `go run ...@version` — do not assume a system install.

## Architecture

The root package is a flat Go module (`github.com/raghavendra-talur/easyshift`) with one subpackage for the CLI entry point (`cmd/easyshift`). Top-level files are organized by responsibility, not by layer:

- `config.go` — `Config` singleton (loaded via `sync.Once` in `GetConfig`), persisted as JSON at `~/.config/easyshift/config.json`. Holds the list of clusters and the `GlobalState` (used IPs/MACs). Also owns `InitLogging`, which writes to `~/.config/easyshift/easyshift.log`.
- `cluster.go` — `ClusterManager` singleton orchestrates the create/start/stop/delete lifecycle, delegating to the install / libvirt / network managers.
- `install.go` — `InstallManager` downloads `openshift-install` and `openshift-client` tarballs from `mirror.openshift.com`, generates `install-config.yaml` from an embedded template, then shells out to `openshift-install` to produce ignition configs.
- `libvirt.go` — wraps `virsh` and `virt-install`. VMs are named `<role>-<index>-<cluster>` (e.g. `master-0-mycluster`).
- `network.go` — `NetworkManager` allocates IPs from the fixed `192.168.1.5`–`192.168.1.20` range and generates MACs with the `52:54:00` (QEMU OUI) prefix.
- `http.go` — `HTTPServer` serves ignition files at port `9393` so RHCOS nodes can pull them during bootstrap.

### Key constraints baked into validation (`cluster.go:249`)

- `MasterCount` **must** be 1 — only single-master is supported.
- `WorkerCount` ≤ `DefaultWorkersMax` (3).
- Total active clusters ≤ `DefaultClustersMax` (3).

### Singletons & locking

`Config`, `ClusterManager`, and `NetworkManager` are all `sync.Once` singletons. `Config` embeds `sync.RWMutex` and `ClusterManager` embeds it too. There was a recent deadlock involving the config lock (commits `4911162`, `8c81b6d`) — be careful when calling `c.save()` / `c.load()`: `load()` takes the write lock, `save()` does not, and `save()` is called from inside `load()`. Audit lock ownership before adding new locking around config persistence.

### External tooling assumed on PATH

`virsh`, `virt-install`, `ssh-keygen`, `tar` (see `utils.go:checkRequiredCommands`). Code does not currently call `checkRequiredCommands` before shelling out — failures surface as `command failed` from `execCmd`.

### Configuration flow

1. `GetConfig()` (called once from `main`) loads `config.json`; if absent, writes defaults.
2. `InitLogging(debug)` runs in cobra's `PersistentPreRun` and opens the log file in append mode — config dir is created here too if missing.
3. Cluster CRUD methods mutate the in-memory `Config.Clusters` slice and then call `cm.config.save()` to persist.
