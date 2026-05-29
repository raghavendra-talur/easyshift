# Usage

This is the command reference and the cluster lifecycle. For when to choose NAT
vs bridge, see **[networking.md](networking.md)**; for DNS/TLS automation, see
**[dns-and-tls.md](dns-and-tls.md)**.

## Global flags

| Flag | Meaning |
| --- | --- |
| `-d`, `--debug` | Verbose logging to `~/.config/easyshift/easyshift.log`. |
| `-S`, `--simulate` | Run the whole pipeline against in-memory fakes in a throwaway config dir, printing a trace of every operation a real run would perform. Touches no real libvirt/DNS/state. Great for a dry run. |

## Lifecycle at a glance

```
create ──> (running) ──> stop ──> (stopped) ──> start ──> (running)
   │                                                          │
   └──────────────────────── delete ◄────────────────────────┘
```

`create` is **idempotent and resumable**: re-running it for a non-running
cluster picks up at the first unfinished install stage. `delete` rolls back each
applied stage in reverse and removes the cluster directory.

## Commands

### `create` — provision a cluster

```sh
easyshift create --name demo                      # zero-config NAT + magic DNS
```

Only `--name` is required. Key flags:

| Flag | Default | Notes |
| --- | --- | --- |
| `-n`, `--name` | — | **Required.** Cluster name. |
| `-v`, `--version` | `stable` | OpenShift version, or a channel alias resolved against the mirror. Pass e.g. `4.21.0` to pin. |
| `--network-mode` | `nat` | `nat` or `bridge`. See [networking.md](networking.md). |
| `--magic-dns` | `auto` | `auto` / `sslip.io` / `nip.io` / `off`. Wildcard DNS so names resolve to the master IP with no records. Mutually exclusive with `--dns-provider` and `--base-domain`. |
| `-D`, `--base-domain` | — | Your own base domain (turns magic DNS off). Cluster lives at `<name>.<base-domain>`. |
| `--master-ram` | 32768 | Master RAM (MB). |
| `--storage-pool` | `default` | libvirt pool for the disk and ISO (`virsh pool-list --all`). |
| `-m`, `--masters` | 1 | Must be 1 (SNO). |
| `-w`, `--workers` | 0 | Must be 0 in the current phase. |

**Bridge-mode-only flags** (see [networking.md](networking.md)):

| Flag | Notes |
| --- | --- |
| `--bridge` | Name of an existing host Linux bridge (e.g. `br0`). Required for bridge mode. |
| `--master-mac` | MAC you reserved at your router for the master VM. Required. |
| `--master-ip` | IP the router will hand to that MAC. Required. |
| `--machine-cidr` | Override `machineNetwork`; defaults to the `/24` of `--master-ip`. |

**DNS / TLS flags** (see [dns-and-tls.md](dns-and-tls.md)):

| Flag | Notes |
| --- | --- |
| `--dns-provider` | `cloudflare` to auto-create `api`/`api-int`/`*.apps` records. Token must be set first. |
| `--dns-zone` | Parent zone, if different from `--base-domain`. |
| `--tls-email` | ACME account email; enables Let's Encrypt certs via DNS-01 (requires `--dns-provider`). |
| `--tls-staging` | Use Let's Encrypt staging (untrusted certs, no rate limits) while iterating. |

### `list` — show all clusters

```sh
easyshift list
# - demo.192.168.126.5.sslip.io  state=running  version=4.21.0  nodes=1m/0w
```

### `status <name>` — diagnose a cluster

Runs read-only checks and prints a report: VM state; (bridge mode) ARP for the
master MAC and that `api`/`api-int`/`*.apps` DNS resolves to the master; the API
port `6443` by IP; the API reachable via DNS; plus the tail of
`.openshift_install.log`. Each failing check includes a hint.

```sh
easyshift status demo
```

### `start` / `stop <name>`

```sh
easyshift stop demo     # graceful shutdown of all nodes
easyshift start demo    # boot them back up
```

### `delete <name>`

Stops the cluster if running, rolls back every applied install stage (VMs,
libvirt artifacts, DNS records, IP/MAC reservations), and removes the cluster
directory.

```sh
easyshift delete demo
```

### `pull-secret` / `dns`

Credential management — see [configuration.md](configuration.md).

```sh
easyshift pull-secret set <file|->     easyshift pull-secret show
easyshift dns set <provider> <file|->  easyshift dns show <provider>
```

## Using the cluster

The admin kubeconfig is written per cluster:

```sh
export KUBECONFIG=~/.config/easyshift/clusters/demo/auth/kubeconfig
oc get nodes
oc get clusterversion
```

NAT-mode clusters are reachable **from the host** (and from each other on the
shared network). Bridge-mode clusters are reachable from anywhere on your LAN.
See [networking.md](networking.md) for the details and tradeoffs.
