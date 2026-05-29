# Providers

Providers are the concrete implementations of the behavioral interfaces. They
are the only packages that talk to the outside world (libvirt, the OpenShift
mirror, DNS APIs, ACME, the host). Stages depend on the *interfaces*; `app`
picks the *implementations*.

## The interface/implementation split

- **`interfaces/`** declares every behavioral interface and the spec/DTO types
  they exchange (`VMManager`, `NetworkProvisioner`, `Installer`, `DNSManager`,
  `CertIssuer`, `HostInspector`, …). It depends only on `config`.
- **`providers/<x>/`** implements one or more of those interfaces. Each provider
  is self-contained: it imports `config` and `interfaces`, **never another
  provider**, **never a stage**.
- **`app`** assembles a `Deps` (`interfaces/deps.go`) from concrete providers and
  hands each stage exactly the interfaces it needs.

This keeps the dependency graph acyclic and means any provider can be swapped
for a fake without touching stage code.

## Inventory

| Package | Implements | Notes |
| --- | --- | --- |
| `providers/exec` | `CommandRunner` | Runs external commands; the base most others build on. |
| `providers/libvirt` | `VMManager`, `NetworkProvisioner` | Wraps `virsh`/`virt-install` on `qemu:///system`. Owns the shared-NAT network logic and the network XML builder. |
| `providers/openshift` | `Installer` + version helpers | Downloads `openshift-install`/`oc`/`coreos-installer`, renders `install-config.yaml`, resolves channel aliases against the mirror. |
| `providers/fileserver` | `FileServer` | Local HTTP server (port 9393) that serves ignition to booting nodes. |
| `providers/dns` | `DNSResolver`, `DNSManager` | Resolver uses `dig`; manager uses libdns (Cloudflare) to upsert/delete records. |
| `providers/tls` | `CertIssuer` (via `NewCertIssuer`) | lego/ACME DNS-01 issuance, with per-provider/per-env account persistence. |
| `providers/host` | `HostInspector`, `HostnameInjector` | Reads `/sys/class/net`, ARP, disk, CPU flags; injects node hostname over SSH. |
| `providers/csr` | `CSRApprover` | Approves pending kubelet CSRs via `oc`. |
| `providers/fakes` | all of the above | In-memory test doubles; see below. |

## Adding or extending a provider

1. Define (or extend) the interface in `interfaces/` — keep it narrow; a stage
   should depend on the smallest surface it needs.
2. Implement it in a new `providers/<x>/` package (or an existing one if it's the
   same external system). Don't reach into other providers.
3. Add a fake in `providers/fakes` that records calls and exposes knobs to force
   errors.
4. Add the field to `interfaces/deps.go:Deps`, populate it in
   `app/deps.go:NewProductionDeps`, and inject it where needed in
   `buildStages`.

### Designing for extensibility

Follow the existing conventions:

- **String-valued options over booleans** when more variants are foreseeable.
  `--magic-dns` and `--network-mode`/`--dns-provider` are strings so new
  services/providers slot in without a new flag or a breaking signature change.
- **Provider selected by a stored credential + a flag value**, e.g.
  `--dns-provider cloudflare` maps to a token at a provider-keyed path. Adding a
  second DNS provider means a new libdns backend + a new token key, not a new
  interface.

## Fakes (`providers/fakes`)

The fakes back both `--simulate` and the unit tests. Conventions:

- Each fake **records** what it was asked to do (e.g. the network fake exposes
  `Ensured []NetworkSpec` and `Added/Removed []HostCall`) so tests assert on
  intent.
- Fakes expose **error knobs** (e.g. `CheckAccessErr`, `StoragePoolErr`, a
  `RunFunc` hook on the command runner) so tests can force failure paths and
  verify rollback/cleanup.
- `fakes.All()` returns a fully-wired `Deps` plus a `Bundle`; `Bundle.WriteTrace`
  renders the operation trace that `--simulate` prints.

When you add a provider, keep its fake's recording shape parallel to the real
calls so tests read naturally — see [testing.md](testing.md).
