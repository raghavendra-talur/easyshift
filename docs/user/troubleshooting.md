# Troubleshooting

Start with the two built-in tools, then the specific failure modes below.

## First steps

- **`easyshift status <name>`** — runs read-only checks (VM state; bridge-mode
  ARP and DNS; API port `6443` by IP and via DNS) and prints a hint for each
  failing check, plus the last 20 lines of the installer log.
- **The log** — `~/.config/easyshift/easyshift.log`. Re-run with `--debug` for
  verbose output.
- **The installer log** —
  `~/.config/easyshift/clusters/<name>/.openshift_install.log`.
- **The VM console** — `sudo virsh -c qemu:///system console master-0-<name>`
  shows boot/bootstrap progress directly.

## Resuming a failed install

`create` is resumable. If a run fails partway, fix the underlying cause and just
run the same `easyshift create --name <name>` again — it picks up at the first
unfinished stage rather than starting over. If you'd rather start clean,
`easyshift delete <name>` first.

## Common failure modes

### Can't reach libvirt / permission denied

`virsh -c qemu:///system list` must work for your user. Ensure you're in the
`libvirt` group and have re-logged in. See
[installation.md](installation.md#libvirt--kvm).

### Storage pool not found

The default pool is named `default`, but some hosts name the pool at
`/var/lib/libvirt/images` differently (e.g. `images`). List yours and pass it:

```sh
virsh -c qemu:///system pool-list --all
easyshift create --name demo --storage-pool images
```

### Bridge mode: bridge has no slaves / is down

The create preflight rejects a bridge that doesn't exist, has no enslaved NIC,
or isn't up — VMs on such a bridge have no path to the LAN. The error includes
the exact fix, e.g.:

```sh
sudo nmcli con add type bridge-slave ifname <NIC> master br0
sudo ip link set br0 up
```

### Bridge mode: node never gets the right IP

If `status` shows the master MAC resolving to the wrong IP (or none), your
router didn't honor the DHCP reservation. Fix the reservation so the master MAC
leases exactly `--master-ip`, then `ping` the IP once to populate ARP and re-run
`status`.

### Bridge mode: DNS records don't resolve

`api`/`api-int`/`*.apps` must resolve to the master IP. Create them at your DNS
host, or use `--dns-provider cloudflare` to have easyshift manage them
(see [dns-and-tls.md](dns-and-tls.md)).

### API port 6443 not reachable yet

If the VM is running but `6443` isn't answering, the cluster is likely still
bootstrapping. SNO bootstrap-in-place takes a while; watch progress on the VM
console or in `.openshift_install.log`. Installs can run for an hour-plus before
the API is up.

### NAT mode: install hangs, kubelet reports the wrong node IP

If a NAT install never finishes and the VM console / `.openshift_install.log`
shows kubelet complaining that its node IP (e.g. `192.168.126.250`) "not found
in the host's network interfaces", the master picked up a stale dynamic DHCP
lease instead of its reserved address. easyshift now pins the master IP
statically, but a NAT network created by an older version keeps its original
(overlapping) DHCP range and accumulated leases. Reset it:

```sh
easyshift nat-network reset --dry-run   # confirm an outdated range / stale leases
easyshift nat-network reset             # recreate the network cleanly
```

Then re-run `easyshift create`.

### TLS cert is untrusted

You're using `--tls-staging` (staging certs are signed by an untrusted root).
Re-run without `--tls-staging` for a browser-trusted production cert — see
[dns-and-tls.md](dns-and-tls.md#--tls-staging-do-this-first).

## Dry run before committing real resources

To see exactly what a command *would* do without touching libvirt, DNS, or your
real state, add `--simulate`. It runs the whole pipeline against in-memory fakes
in a throwaway config dir and prints a trace of every operation.

```sh
easyshift create --name demo --simulate
```

## Cleaning up

`easyshift delete <name>` is the right way to remove a cluster: it stops the
VMs, rolls back every applied stage (VMs, libvirt artifacts, DNS records, and
the global IP/MAC reservations), and removes the cluster directory. Avoid
hand-editing `config.json` to remove a cluster — you'll leak those reservations
and libvirt objects.

If a crashed run left the shared NAT network out of sync anyway — orphaned
reservations, leaked allocations, or an outdated DHCP range — reconcile it with
`easyshift nat-network reset` (use `--dry-run` first to preview).
