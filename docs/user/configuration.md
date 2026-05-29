# Configuration

easyshift keeps all of its state under a single config directory. There is no
global config file to hand-edit for day-to-day use — you drive everything
through CLI flags and a few `set` subcommands.

## The config directory

Default: **`~/.config/easyshift`** (created mode `0700` on first run). If `HOME`
is unset, a temp-dir fallback is used.

```
~/.config/easyshift/
├── config.json                 # global config + cluster list + allocated IPs/MACs (mode 0600)
├── easyshift.log               # append-mode log (see --debug)
├── pull-secret                 # your OpenShift pull secret (mode 0600)
├── cloudflare-token            # DNS provider API token, if set (mode 0600)
├── acme/                       # Let's Encrypt account keys (per provider, staging vs prod)
└── clusters/
    └── <name>/                 # per-cluster openshift-install working dir
        ├── auth/kubeconfig     # admin kubeconfig for the cluster
        ├── state.json          # which install stages have been applied (resume/rollback)
        ├── master.iso          # embedded bootstrap-in-place ISO
        └── .openshift_install.log
```

### config.json

Written automatically; you rarely edit it by hand. It holds:

- `configDir`, `logFile`, `webPort` (default **9393**, the local HTTP server
  that serves ignition to booting nodes), `debug`.
- `clusters[]` — the persisted spec for each cluster (name, domain, version,
  network mode, sizing, DNS/TLS settings, allocated IPs/MACs, install state).
- `globalState` — `usedIPs` / `usedMACs` allocated across **all** clusters so
  NAT clusters never collide, plus `activeCluster`.

Because allocation is global, prefer `easyshift delete` over hand-editing
`config.json` — delete frees the IP/MAC reservations and rolls back libvirt
state for you.

## Pull secret

Required before any `create`. Stored once at `~/.config/easyshift/pull-secret`
(mode `0600`); it is **not** kept in `config.json`.

```sh
easyshift pull-secret set ~/Downloads/pull-secret.txt   # from a file
easyshift pull-secret set -                             # from stdin
easyshift pull-secret show                              # print the stored path
```

## DNS provider credentials

Only needed if you want easyshift to **create DNS records for you** (bridge mode
with `--dns-provider`) or issue **Let's Encrypt** certs. Zero-config NAT mode
with magic DNS needs none of this.

Currently supported provider: **`cloudflare`**. Use a scoped API **token** (not
the global key) with `Zone:DNS:Edit` on the relevant zone.

```sh
easyshift dns set cloudflare ~/cloudflare-token.txt     # from a file
easyshift dns set cloudflare -                          # from stdin
easyshift dns show cloudflare                           # print the stored path
```

The token is stored at `~/.config/easyshift/cloudflare-token` (mode `0600`).
See **[dns-and-tls.md](dns-and-tls.md)** for how it's used.

## Defaults you can override at create time

| Setting | Default | Flag |
| --- | --- | --- |
| OpenShift version | `stable` (resolved to the current z-stream) | `--version` |
| Network mode | `nat` | `--network-mode` |
| Magic DNS | `auto` (NAT → `sslip.io`, bridge → off) | `--magic-dns` |
| Master RAM | 32768 MB | `--master-ram` |
| Master disk | 120 GiB | (not a flag; sizing is fixed per role) |
| Storage pool | `default` | `--storage-pool` |
| Masters / workers | 1 / 0 | `--masters` / `--workers` |

Limits baked in: **max 3 clusters**, **single-node only** (1 master, 0 workers
in the current phase).

See **[usage.md](usage.md)** for the complete flag reference.
