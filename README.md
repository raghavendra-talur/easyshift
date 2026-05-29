# easyshift

An opinionated OpenShift installer for developers.

`easyshift` provisions single-node OpenShift (SNO) clusters as libvirt VMs on
your local Linux host. It is a small Go CLI that wraps `openshift-install`,
`virsh`/`virt-install`, and (optionally) public DNS + Let's Encrypt so that
`easyshift create` takes you from nothing to a reachable cluster with one
command. It is opinionated on purpose: single master, sensible defaults, and a
zero-config networking mode so you don't have to think about DNS for a throwaway
cluster.

## Features

- **One-command SNO installs** — bootstrap-in-place from an embedded ignition
  ISO; no separate bootstrap node.
- **Zero-config NAT mode** — VMs run behind a libvirt NAT network with magic
  wildcard DNS (`sslip.io`/`nip.io`), so cluster names resolve to the master IP
  with no DNS records to manage.
- **Shared NAT network for multi-cluster work** — all NAT clusters share one L2
  segment and can reach each other (built for disaster-recovery topologies like
  hub/spoke and replication).
- **Bridge mode for LAN-reachable clusters** — attach VMs to a host Linux bridge
  so the cluster gets a real LAN IP, with optional automated DNS (Cloudflare)
  and browser-trusted TLS (Let's Encrypt via ACME DNS-01).
- **Idempotent, resumable installs** — each install is a sequence of stages
  tracked in `state.json`; a failed run resumes from where it stopped, and
  `delete` rolls each stage back.
- **Provider-agnostic by design** — DNS, TLS, libvirt, and host access sit
  behind interfaces, and a `--simulate` mode runs the whole pipeline against
  in-memory fakes.

## Requirements

- A Linux host with KVM (CPU virtualization: `vmx`/`svm`).
- libvirt with the `qemu:///system` connection, plus `virsh` and `virt-install`
  on `PATH`. You need permission to use `qemu:///system` (libvirt-group
  membership) — root is not required.
- `ssh-keygen`, `tar`, and `dig` on `PATH`.
- An OpenShift pull secret (from <https://console.redhat.com/openshift/install/pull-secret>).

## Install

`easyshift` builds from source. The Makefile is the source of truth (it runs
`go vet` and a gofmt check before building):

```sh
make build      # produces ./easyshift at the repo root
```

For prerequisites, building, and first-run setup in detail, see
**[docs/user/installation.md](docs/user/installation.md)**.

## Quickstart

```sh
# 1. Store your pull secret once.
easyshift pull-secret set ~/Downloads/pull-secret.txt

# 2. Create a zero-config NAT cluster (magic DNS, no records to manage).
easyshift create --name demo

# 3. Check on it / use it.
easyshift status demo
export KUBECONFIG=~/.config/easyshift/clusters/demo/auth/kubeconfig
oc get nodes
```

To configure and use easyshift beyond the quickstart — networking modes, DNS
and TLS automation, and the full command reference — start at the
**[user docs](docs/user/)**.

## Documentation

| For… | Start here |
| --- | --- |
| Running clusters | [docs/user/](docs/user/) — install, configure, use, troubleshoot |
| Contributing / internals | [docs/dev/](docs/dev/) — architecture, stages, providers, testing |

The docs index is at **[docs/README.md](docs/README.md)**.

## Contributing

Contributions are welcome. See **[CONTRIBUTING.md](CONTRIBUTING.md)** for the
quick version and **[docs/dev/contributing.md](docs/dev/contributing.md)** for
the full workflow.

## License

See [LICENSE](LICENSE).
