# Networking

easyshift has two network modes. The right one depends on **who needs to reach
the cluster** and **how much DNS you want to deal with**.

| | NAT mode (`--network-mode nat`, default) | Bridge mode (`--network-mode bridge`) |
| --- | --- | --- |
| Where VMs live | Behind a libvirt NAT network | Attached to a host Linux bridge on your LAN |
| Reachable from | The host, and other easyshift clusters | Anywhere on the LAN |
| DNS | Magic wildcard DNS (`sslip.io`), zero records | Real records you provide (or automate via Cloudflare) |
| Setup effort | None | Host bridge + router DHCP reservation + DNS |
| Good for | Quick/throwaway clusters, multi-cluster DR labs | Clusters you reach from other machines, real TLS |

## NAT mode (default)

```sh
easyshift create --name demo            # that's the whole command
```

What happens:

- **One shared libvirt NAT network** (`easyshift-nat`, subnet
  `192.168.126.0/24`) is created on the first NAT cluster and reused by every
  NAT cluster after it. Each master gets a distinct, **statically pinned IP**
  (`.5`, `.6`, …) embedded in its boot ISO, so the node never depends on a DHCP
  lease for its address — see "Static IP" below. A DHCP reservation is still
  recorded as a backstop, and the dynamic DHCP pool (`.100`–`.254`) is kept
  clear of those reserved addresses. The hostname (`master-0-<name>...`) is set
  over SSH during install.
- **Magic DNS** (`sslip.io`) gives the cluster a base domain derived from the
  master IP, e.g. master `192.168.126.5` →
  `demo.192.168.126.5.sslip.io`. `sslip.io`/`nip.io` are wildcard resolvers:
  `anything.<ip>.sslip.io` resolves to `<ip>`, so `api`, `api-int`, and
  `*.apps` all resolve correctly with **no DNS records and no DNS server of
  your own**.

Because it's a NAT network, the cluster is reachable **from the host** but not
from other machines on your LAN. That's usually what you want for local dev.

### Magic DNS options

`--magic-dns` is string-valued so more services can be added later:

| Value | Effect |
| --- | --- |
| `auto` (default) | NAT mode → `sslip.io`; bridge mode → off. |
| `sslip.io` | Force sslip.io (any mode), keyed on the master IP. |
| `nip.io` | Force nip.io. |
| `off` | Disabled — use `--base-domain` plus manual/automated DNS. |

Magic DNS is mutually exclusive with `--dns-provider` and `--base-domain`: it
*derives* the domain from the IP, so there's nothing to manage and nothing to
override.

## Multi-cluster / disaster-recovery topologies

The NAT network is **global state**, not per-cluster. All NAT clusters share one
L2 segment, so they can reach each other directly — exactly what you need for
DR-style labs (hub/spoke, replication, failover between two clusters).

```sh
easyshift create --name hub
easyshift create --name spoke      # same shared network; gets its own IP/hostname
```

Deleting one cluster removes **only its** DHCP reservation; the shared network
and the other cluster's reservation stay intact. The network is never torn down
by deleting a single cluster.

> Capacity: up to 3 clusters, each gets a distinct address from the shared
> subnet.

## Bridge mode

Use bridge mode when the cluster must be reachable from other machines on your
LAN, or when you want browser-trusted TLS on a real domain.

You provide:

1. **An existing host Linux bridge** connected to your LAN (e.g. `br0`) with a
   physical NIC enslaved and the bridge up. easyshift's preflight checks that
   the bridge exists, has at least one slave interface, and is up — and tells
   you how to fix it if not.
2. **A free IP on your LAN** for the master (`--master-ip`), outside your
   router's DHCP pool. A router DHCP reservation for the master's MAC still
   works but is no longer required — see "Static IP" below.
3. **DNS** for `api.<fqdn>`, `api-int.<fqdn>`, and `*.apps.<fqdn>` pointing at
   that IP — either created by you, or automated with `--dns-provider`
   (see [dns-and-tls.md](dns-and-tls.md)).

```sh
easyshift create --name lab \
  --network-mode bridge \
  --bridge br0 \
  --master-mac 52:54:00:aa:bb:cc \
  --master-ip 192.168.1.50 \
  --base-domain lab.example.com
```

`--machine-cidr` defaults to the `/24` of `--master-ip`; override it if your LAN
uses a different prefix.

### Static IP (no DHCP race)

In **both** network modes the master is configured with a **static IP** rather
than relying on DHCP. easyshift embeds a NetworkManager keyfile (matched on the
master's MAC) into the boot ISO with `coreos-installer iso network embed`, so the
node pins its address from its very first boot — including while the OpenShift
bootstrap runs. This eliminates a subtle failure: if the node briefly took a
DHCP-pool address before its reservation kicked in, etcd/kubelet would bake that
wrong address (a wrong `nodeIP`) into the cluster permanently and the install
would hang. The static config also propagates into the installed system, so the
IP survives the bootstrap reboot.

- **Bridge mode** pins `--master-ip` (gateway/DNS from `--gateway`/`--dns`).
- **NAT mode** pins the auto-allocated address (`.5`, `.6`, …), with gateway and
  DNS defaulting to the NAT network's `.1`. Because the node no longer receives
  a DHCP-provided hostname (option 12), easyshift sets it over SSH during the
  install instead.

The gateway and DNS server default to the `.1` of `--machine-cidr`. Override them
when your network differs:

```sh
  --gateway 192.168.1.254 \
  --dns 192.168.1.1,1.1.1.1     # comma-separated; defaults to --gateway
```

The `verify-master-ip` stage remains as a fast safety net: it aborts early if the
node still doesn't answer on its reserved IP (e.g. an IP conflict on the LAN).

### Creating a bridge (one-time host setup)

If you don't already have a LAN bridge, create one and enslave your NIC, e.g.
with NetworkManager:

```sh
sudo nmcli con add type bridge ifname br0 con-name br0
sudo nmcli con add type bridge-slave ifname <NIC> master br0
sudo nmcli con up br0
```

If the bridge exists but has no slaves (or is down), VMs attached to it have no
path to the LAN — the create preflight will catch this and print the exact
`nmcli`/`ip link` command to fix it.
