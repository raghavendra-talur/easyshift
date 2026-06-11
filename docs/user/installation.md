# Installation

easyshift runs on a Linux host and provisions OpenShift VMs locally through
libvirt/KVM. There are no prebuilt releases yet — you build the binary from
source.

## Prerequisites

### Hardware / virtualization

- A CPU with virtualization extensions (`vmx` on Intel, `svm` on AMD). Check:
  ```sh
  grep -Eoc '(vmx|svm)' /proc/cpuinfo   # > 0 means supported
  ```
- Enough RAM and disk for a single-node cluster. Defaults are **32 GiB RAM** and
  a **120 GiB** master disk per cluster, so size the host accordingly (you can
  lower RAM with `--master-ram`).

### libvirt / KVM

easyshift talks to the **`qemu:///system`** libvirt instance and shells out to
`virsh` and `virt-install`. On Fedora/RHEL:

```sh
sudo dnf install -y libvirt virt-install qemu-kvm
sudo systemctl enable --now libvirtd
```

You need permission to use `qemu:///system`. **Root is not required** — adding
your user to the `libvirt` group is enough:

```sh
sudo usermod -aG libvirt "$USER"
# log out / back in (or `newgrp libvirt`) for the group to take effect
virsh -c qemu:///system list --all     # should succeed without sudo
```

> Note: the older `CLAUDE.md` text about "refuses to run unless root" is stale;
> the current binary does not enforce root. Stages that touch `qemu:///system`
> will surface a clear permission error if your account can't reach libvirt.

### Other tools on PATH

`ssh-keygen`, `tar`, and `dig` (from `bind-utils`/`dnsutils`). The
OpenShift binaries (`openshift-install`, `oc`, `coreos-installer`) are
**downloaded automatically** per cluster — you do not install them yourself.

### A toolchain to build

Go **1.25+** (see `go.mod`). The build also runs pinned `golangci-lint` and
`checkmake` via `go run`, so they don't need to be installed separately.

### A pull secret

Download your OpenShift pull secret from
<https://console.redhat.com/openshift/install/pull-secret>. You store it once;
see [configuration.md](configuration.md#pull-secret).

## Build

The Makefile is the source of truth — it runs `go vet` and a gofmt check before
building, which a bare `go build` would skip.

```sh
git clone https://github.com/TheEasyShift/easyshift
cd easyshift
make build        # produces ./easyshift at the repo root
```

Other useful targets:

| Target | Does |
| --- | --- |
| `make` / `make build` | vet + gofmt check + build |
| `make test` | unit tests |
| `make check` | lint + build + test (run before pushing) |
| `make clean` | remove the binary |

Put the binary on your `PATH` if you like:

```sh
sudo install -m 0755 easyshift /usr/local/bin/easyshift
```

## First run

The first invocation creates the config directory (`~/.config/easyshift`,
mode `0700`) and writes a default `config.json`. Store your pull secret, then
create a cluster:

```sh
easyshift pull-secret set ~/Downloads/pull-secret.txt
easyshift create --name demo
```

Next: **[configuration.md](configuration.md)** for what lives on disk and how to
set credentials, then **[usage.md](usage.md)** for the full command set.
