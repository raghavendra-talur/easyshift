# Pull secret login: fetch from Red Hat SSO during `create`

**Date:** 2026-06-11
**Status:** Approved design, pending implementation plan

## Problem

When no pull secret is configured, `easyshift create` fails with a message
that only says where the file should be and which command stores it
(`config/pullsecret.go`). Many users do not know what a pull secret is or
where to get one; the console.redhat.com link lives only in the README. The
manual path is extra painful on a headless machine: download the secret on a
laptop, then copy the file over.

## Solution overview

When `easyshift create` finds no pull secret and stdin is a TTY, it explains
what a pull secret is and offers two paths:

```
No pull secret configured.

A pull secret is a free credential from your Red Hat account that lets the
installer download OpenShift container images. Two ways to set it up:

  1. Log in to your Red Hat account now (recommended) — you'll get a short
     code to enter at sso.redhat.com/device from any browser, e.g. your laptop.
  2. Download it yourself from
     https://console.redhat.com/openshift/install/pull-secret
     and run: easyshift pull-secret set <file>

Log in and fetch it now? [Y/n]
```

- **Yes** → run the OAuth device-code flow (print verification URL + user
  code, poll for authorization), fetch the pull secret from the OCM API,
  validate it with the existing `ValidatePullSecretJSON`, store it via the
  existing `WritePullSecret` (mode 0600), and continue the create run without
  restarting.
- **No** → print the option-2 manual instructions again and exit non-zero.
- **Non-TTY** (CI, scripts) → no prompt, no polling; fail immediately with
  the same guidance text.

The same flow is exposed as a standalone command, `easyshift pull-secret
login`, for users who want to set up ahead of time. `easyshift pull-secret
set <file>` is unchanged and remains the fallback for air-gapped hosts,
service accounts, or if Red Hat's endpoints change.

The device-code grant is specifically suited to headless hosts: the machine
running easyshift only prints a URL and a short code; the user authorizes
from any browser on any device where they already have a Red Hat SSO session.

## Mechanics

1. **Device authorization request:** form POST to
   `https://sso.redhat.com/auth/realms/redhat-external/protocol/openid-connect/auth/device`
   with the public `ocm-cli` client id (the same client `ocm login
   --use-device-code` uses). Response carries `verification_uri`,
   `user_code`, `device_code`, `interval`, `expires_in`.
2. **Token polling:** form POST to the realm's token endpoint with
   `grant_type=urn:ietf:params:oauth:grant-type:device_code` at the server's
   stated interval, honoring `authorization_pending` / `slow_down`, until
   success, denial, or device-code expiry (~10 min).
3. **Pull secret fetch:** `POST
   https://api.openshift.com/api/accounts_mgmt/v1/access_token` with the
   bearer access token; the response body is the pull secret JSON (the same
   API console.redhat.com serves it from).

No refresh token or login state is persisted — this is a one-shot fetch. The
only artifact written to disk is the pull secret itself, through the existing
`WritePullSecret` path. Plain `net/http`; no new module dependencies.

**Failure policy:** device-code expiry, authorization denial, and any
API/network error do not retry; they degrade to the option-2 manual
instructions and a non-zero exit. The `ocm-cli` client id and the
`access_token` endpoint belong to Red Hat — if they change, the flow breaks,
which is why every failure path lands on the manual instructions, which
cannot break.

## Architecture placement

Follows the existing acyclic dependency flow
(`config ← interfaces ← providers ← app ← cmd`):

- **`interfaces/`** — a new `PullSecretFetcher` interface, roughly:

  ```go
  type DeviceAuthPrompt struct {
      VerificationURI string
      UserCode        string
  }

  type PullSecretFetcher interface {
      // StartDeviceAuth begins the device-code grant and returns what the
      // user must be shown. The caller owns all printing.
      StartDeviceAuth(ctx context.Context) (DeviceAuthPrompt, error)
      // WaitAndFetch polls until the user authorizes (or the code expires),
      // then fetches and returns the raw pull secret bytes.
      WaitAndFetch(ctx context.Context) ([]byte, error)
  }
  ```

- **`providers/redhat/`** — the concrete implementation. Self-contained per
  provider rules (imports no other provider, no stage). Endpoint base URLs
  are constructor parameters so tests can point at `httptest.Server`.
- **`providers/fakes/`** — a fake fetcher for unit tests.
- **`cmd/easyshift/`** — owns all interactivity: TTY detection, the Y/n
  prompt, printing the verification URL + code, a waiting indicator while
  polling. The `create` command runs this flow *before* calling
  `ClusterManager.Create`. Also adds the `pull-secret login` subcommand.
- **`app/`** — unchanged. `ClusterManager.Create`'s existing
  `EnsurePullSecret` check stays as a non-interactive backstop, so `app/`
  and `stages/` remain prompt-free.

## Testing

- **`providers/redhat`** unit tests against `httptest.Server`: happy path;
  `authorization_pending` then success; `slow_down` handling; expired device
  code; authorization denied; malformed pull secret response (must fail
  `ValidatePullSecretJSON` downstream).
- **cmd-level** tests with the fake fetcher: TTY yes-path stores the secret
  and proceeds; no-path prints manual instructions and exits non-zero;
  non-TTY fails immediately with guidance.
- **`--simulate`** keeps pre-planting its fake pull secret, so the simulated
  pipeline never reaches the prompt.

## Out of scope

- Persisting SSO tokens / a `logout` command — one-shot fetch only.
- A flag to force or suppress the prompt in non-TTY runs (can be added later
  if CI users ask).
- Opening a browser automatically — the target user is on a headless host.

## Documentation updates

- `docs/user/configuration.md`: document `pull-secret login` alongside
  `pull-secret set`.
- README quick-start: mention that `create` offers to fetch the pull secret
  interactively, with `pull-secret set` as the manual alternative.
