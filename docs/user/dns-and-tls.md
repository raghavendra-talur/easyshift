# DNS and TLS automation

This page covers the **optional** automation for bridge-mode clusters that use a
real domain: having easyshift create the DNS records for you, and issuing
browser-trusted Let's Encrypt certificates.

> Not using a real domain? Zero-config NAT mode with magic DNS
> ([networking.md](networking.md)) needs none of this — skip this page.

## When you need it

A bridge-mode cluster needs three DNS names resolving to the master IP:

- `api.<name>.<base-domain>`
- `api-int.<name>.<base-domain>`
- `*.apps.<name>.<base-domain>`

You can create these by hand at your DNS host, or let easyshift manage them with
`--dns-provider`.

## Automated DNS records (`--dns-provider`)

Currently supported provider: **`cloudflare`**.

1. Create a scoped Cloudflare API **token** (not the global key) with
   `Zone:DNS:Edit` permission on the zone that owns your domain.
2. Store it once:
   ```sh
   easyshift dns set cloudflare ~/cloudflare-token.txt
   ```
3. Create the cluster with the provider enabled:
   ```sh
   easyshift create --name lab \
     --network-mode bridge --bridge br0 \
     --master-mac 52:54:00:aa:bb:cc --master-ip 192.168.1.50 \
     --base-domain lab.example.com \
     --dns-provider cloudflare
   ```

easyshift creates the `api`/`api-int`/`*.apps` A records pointing at the master
IP during install, and removes them on `easyshift delete`.

### `--dns-zone`

The zone defaults to `--base-domain`. Override it when your zone is a *parent*
of the base domain — e.g. base domain `dev.example.com` but the Cloudflare zone
is `example.com`:

```sh
--base-domain dev.example.com --dns-zone example.com
```

## Let's Encrypt TLS (`--tls-email`)

Setting `--tls-email` enables certificate issuance for `api.<fqdn>` and
`*.apps.<fqdn>` via **ACME DNS-01**. The DNS-01 challenge reuses your
`--dns-provider` token to write the validation records, so:

> `--tls-email` **requires** `--dns-provider`.

```sh
easyshift create --name lab \
  --network-mode bridge --bridge br0 \
  --master-mac 52:54:00:aa:bb:cc --master-ip 192.168.1.50 \
  --base-domain lab.example.com \
  --dns-provider cloudflare \
  --tls-email you@example.com \
  --tls-staging          # do your first run against staging
```

### `--tls-staging`: do this first

Let's Encrypt's **production** endpoint has strict rate limits. Its **staging**
endpoint has generous limits but issues certs signed by an *untrusted* root
(your browser will warn). The recommended flow:

1. Run with `--tls-staging` and confirm the whole pipeline works end to end.
2. Delete, then re-run **without** `--tls-staging` for a browser-trusted cert.

ACME account keys are persisted per provider and per environment (staging vs
production) under `~/.config/easyshift/acme/`, so switching between them doesn't
clobber the other's account.

### Your kubeconfig keeps working

Once `api.<fqdn>` serves the Let's Encrypt cert, the install-generated admin
kubeconfig — which pins the cluster's *internal* CA — would otherwise fail with
`certificate signed by unknown authority`. The `apply-tls-certs` stage handles
this for you: after the cert is applied it rewrites
`clusters/<name>/auth/kubeconfig` to drop the embedded CA, so `oc` validates the
public cert through your system trust store with no extra flags. The original is
preserved next to it as `kubeconfig.internal-ca` for break-glass use or for
talking to the internal `api-int.<fqdn>` endpoint.

> The rewrite happens immediately, but the API may take a few minutes to finish
> rolling out the new serving cert. Until it does, `oc` can briefly report a
> cert error — give the `kube-apiserver` operator time to settle.

## Verifying

After install:

```sh
easyshift status lab        # checks DNS records resolve to the master + API reachability
```

In bridge mode the `status` command verifies that all three records resolve to
the master IP and that the API answers both by IP and via DNS.
