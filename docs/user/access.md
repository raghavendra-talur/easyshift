# Accessing your cluster

## kubectl / oc — zero setup

`easyshift create` merges an admin context named after the cluster into your
kubeconfig (`$KUBECONFIG`'s first path, else `~/.kube/config`) and makes it
the current context. When create finishes, `oc get nodes` just works.

- The context is named `<cluster-name>`; the cluster/user entries are
  prefixed `easyshift-` to avoid colliding with entries you manage yourself.
- A pre-existing context with the same name as your cluster is overwritten.
- `easyshift delete` removes exactly the entries it added and resets
  `current-context` only if it still points at the deleted cluster.

The raw admin kubeconfig stays at
`~/.config/easyshift/clusters/<name>/auth/kubeconfig`; the original
(internal-CA) copy is preserved at `kubeconfig.internal-ca`.

## Console login

The console URL and the kubeadmin password file path are printed at the end
of `create`. Log in as `kubeadmin` with the password from
`~/.config/easyshift/clusters/<name>/auth/kubeadmin-password`.

## TLS: the easyshift local CA

Clusters created **without** `--tls-email` get `api.<fqdn>` and
`*.apps.<fqdn>` certificates signed by a CA generated once per host at
`~/.config/easyshift/ca/`. To make browsers trust the console (no TLS
warnings), run once:

    easyshift trust

This uses `sudo` to install the CA into the system trust store
(Fedora/RHEL: `update-ca-trust`; Debian/Ubuntu: `update-ca-certificates`;
macOS: the System keychain) and — when `certutil` is installed — into the
NSS databases Firefox and Chrome read on Linux. Without `certutil`
(package `nss-tools` on Fedora, `libnss3-tools` on Debian) those browsers
may still warn.

Reverse it anytime with `easyshift trust --uninstall`.

Clusters created **with** `--tls-email` use Let's Encrypt instead and need no
trust step.

Note: the local CA's key lives unencrypted (mode 0600) in your config dir,
and anything it signs is trusted once you run `easyshift trust`. That is the
same trade-off minikube and mkcert make — fine for throwaway dev clusters on
your own machine; don't copy the key elsewhere.

## start waits for the node

`easyshift start` boots the VM, waits for the API, approves pending
certificate signing requests (a node whose VM was off across a kubelet
cert rotation can't rejoin until its renewal CSRs are approved), and returns
once the node is Ready. If convergence times out the cluster stays running —
check `easyshift status <name>`.
