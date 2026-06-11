# Pull Secret Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When no pull secret is configured, `easyshift create` offers to fetch one via the Red Hat SSO OAuth device-code flow (also exposed as `easyshift pull-secret login`), with the manual `pull-secret set` path as the fallback.

**Architecture:** A new `PullSecretFetcher` interface in `interfaces/` is implemented by a new self-contained `providers/redhat` package (plain `net/http`, two SSO form-POST endpoints + one OCM API call). `cmd/easyshift` owns all interactivity (TTY detection, Y/n prompt, printing the device code); `app/` and stages stay prompt-free. A fake fetcher in `providers/fakes` backs unit tests and `--simulate`.

**Tech Stack:** Go stdlib only (`net/http`, `net/url`, `encoding/json`, `httptest`); no new module dependencies. Spec: `docs/superpowers/specs/2026-06-11-pull-secret-login-design.md`.

**Conventions:** Module path is `github.com/TheEasyShift/easyshift`. Commit subjects are `area: lowercase summary` (see `git log`). Every commit: `git commit -s` and end the message with `Assisted-by: Claude Code/claude-fable-5`. Run tests via `go test` per package while iterating; `make check` at the end (it adds gofmt+vet+lint).

---

## File structure

| File | Action | Responsibility |
| --- | --- | --- |
| `interfaces/interfaces.go` | Modify | Add `DeviceAuthPrompt` + `PullSecretFetcher` contract |
| `interfaces/deps.go` | Modify | Add `PullSecret PullSecretFetcher` to the wiring bag |
| `config/paths.go` | Modify | Extract `ValidatePullSecretBytes` from `ValidatePullSecretJSON` |
| `config/pullsecret_validate_test.go` | Create | Tests for byte-level validation |
| `config/pullsecret.go` | Modify | Mention `pull-secret login` in the not-configured error |
| `providers/redhat/fetcher.go` | Create | Device-code grant + OCM pull-secret fetch |
| `providers/redhat/fetcher_test.go` | Create | `httptest.Server` tests for every flow outcome |
| `providers/fakes/fakes.go` | Modify | Fake fetcher; wire into `All()` + `Bundle` |
| `app/deps.go` | Modify | Wire the real fetcher into `NewProductionDeps` |
| `cmd/easyshift/pullsecret_flow.go` | Create | Guidance text, TTY check, Y/n flow, fetch-and-store |
| `cmd/easyshift/pullsecret_flow_test.go` | Create | Flow tests with the fake fetcher |
| `cmd/easyshift/main.go` | Modify | Hook flow into `create`; add `pull-secret login` |
| `docs/user/configuration.md`, `README.md` | Modify | Document the new command |

---

### Task 1: `PullSecretFetcher` interface + `Deps` field

`interfaces/` is a no-behavior package — no unit test; the compiler is the check.

**Files:**
- Modify: `interfaces/interfaces.go` (append at end, after `HostInspector`)
- Modify: `interfaces/deps.go`

- [ ] **Step 1: Add the contract to `interfaces/interfaces.go`**

Append at the end of the file:

```go
// DeviceAuthPrompt is what the user must be shown to complete an OAuth
// device-code login: open the URI on any device and enter the code.
type DeviceAuthPrompt struct {
	VerificationURI string
	UserCode        string
}

// PullSecretFetcher obtains the user's OpenShift pull secret from their
// Red Hat account via the OAuth 2.0 device authorization grant. Call
// StartDeviceAuth, show the returned prompt, then WaitAndFetch, which blocks
// (polling) until the user authorizes, the code expires, or ctx is cancelled.
// The caller owns all printing.
type PullSecretFetcher interface {
	StartDeviceAuth(ctx context.Context) (DeviceAuthPrompt, error)
	WaitAndFetch(ctx context.Context) ([]byte, error)
}
```

- [ ] **Step 2: Add the field to `interfaces/deps.go`**

In the `Deps` struct, after `DNSManager DNSManager`:

```go
	// PullSecret fetches the pull secret from the user's Red Hat account
	// (device-code login). Consumed only by cmd — never by stages.
	PullSecret PullSecretFetcher
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: success, no output.

- [ ] **Step 4: Commit**

```bash
git add interfaces/interfaces.go interfaces/deps.go
git commit -s -m "interfaces: add PullSecretFetcher contract for Red Hat SSO login

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 2: `config.ValidatePullSecretBytes`

The cmd flow must validate fetched bytes *before* writing them to disk. Extract the parsing half of `ValidatePullSecretJSON` (`config/paths.go:97-113`), preserving its exact error messages.

**Files:**
- Modify: `config/paths.go:97-113`
- Test: `config/pullsecret_validate_test.go`

- [ ] **Step 1: Write the failing test**

Create `config/pullsecret_validate_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestValidatePullSecretBytes(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string // empty = expect success
	}{
		{name: "valid", data: `{"auths":{"quay.io":{"auth":"Zm9v"}}}`},
		{name: "not json", data: `not-json`, wantErr: "not valid JSON"},
		{name: "missing auths", data: `{"foo":1}`, wantErr: "missing required 'auths' key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePullSecretBytes([]byte(tt.data))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidatePullSecretBytes() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidatePullSecretBytes() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./config/ -run TestValidatePullSecretBytes -v`
Expected: FAIL — `undefined: ValidatePullSecretBytes`.

- [ ] **Step 3: Implement the extraction**

In `config/paths.go`, replace the whole `ValidatePullSecretJSON` function with:

```go
// ValidatePullSecretJSON parses the persisted pull secret and verifies it is
// JSON with the required "auths" key. Run as a preflight so a malformed
// secret fails fast instead of mid-install.
func ValidatePullSecretJSON(configDir string) error {
	data, err := os.ReadFile(PullSecretPath(configDir))
	if err != nil {
		return fmt.Errorf("read pull secret: %w", err)
	}
	return ValidatePullSecretBytes(data)
}

// ValidatePullSecretBytes verifies data is JSON with the required "auths"
// key. Used to vet a fetched secret before it is written to disk.
func ValidatePullSecretBytes(data []byte) error {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("pull secret is not valid JSON: %w", err)
	}
	if _, ok := parsed["auths"]; !ok {
		return fmt.Errorf("pull secret is missing required 'auths' key (download a fresh secret from console.redhat.com)")
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./config/ -v`
Expected: PASS (the new test and all existing config tests).

- [ ] **Step 5: Commit**

```bash
git add config/paths.go config/pullsecret_validate_test.go
git commit -s -m "config: extract ValidatePullSecretBytes for pre-write validation

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 3: `providers/redhat` fetcher

Self-contained provider (imports only stdlib + `interfaces`, per the provider rules). Endpoint base URLs are constructor parameters so tests point them at an `httptest.Server`.

Protocol notes for the implementer:
- Device endpoint: `POST {sso}/protocol/openid-connect/auth/device`, form body `client_id=ocm-cli`. JSON response: `device_code`, `user_code`, `verification_uri`, `expires_in` (seconds), `interval` (seconds).
- Token endpoint: `POST {sso}/protocol/openid-connect/token`, form body `grant_type=urn:ietf:params:oauth:grant-type:device_code`, `device_code`, `client_id`. Non-200 responses carry `{"error":"<code>"}`: `authorization_pending` (keep polling), `slow_down` (add 5s to the interval, RFC 8628 §3.5), `expired_token` / `access_denied` (terminal).
- Pull secret: `POST {api}/api/accounts_mgmt/v1/access_token` with `Authorization: Bearer <token>`; any 2xx body is the pull secret JSON.
- `interval` uses `*int`, not `int`: the RFC default of 5s applies only when the field is *absent*, and tests rely on an explicit `"interval": 0` for fast polling.
- `slowDownIncrement` is a package `var` so the slow_down test can shrink it; production never touches it.

**Files:**
- Create: `providers/redhat/fetcher.go`
- Test: `providers/redhat/fetcher_test.go`

- [ ] **Step 1: Write the failing tests**

Create `providers/redhat/fetcher_test.go`:

```go
package redhat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeSecret = `{"auths":{"quay.io":{"auth":"Zm9v"}}}`

// testEnv stands up one server answering all three endpoints. tokenResponses
// is consumed one element per token poll; the last element repeats.
type testEnv struct {
	fetcher        *Fetcher
	tokenPolls     atomic.Int64
	tokenResponses []func(w http.ResponseWriter)
	deviceStatus   int
	secretStatus   int
}

func tokenOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"access_token":"tok123"}`))
}

func tokenErr(code string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
}

func newTestEnv(t *testing.T, tokenResponses ...func(w http.ResponseWriter)) *testEnv {
	t.Helper()
	env := &testEnv{tokenResponses: tokenResponses, deviceStatus: http.StatusOK, secretStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/protocol/openid-connect/auth/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") == "" {
			t.Errorf("device request missing client_id form field")
		}
		w.WriteHeader(env.deviceStatus)
		if env.deviceStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"device_code":"dev123","user_code":"ABCD-EFGH",` +
				`"verification_uri":"https://sso.example/device","expires_in":600,"interval":0}`))
		}
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("device_code") != "dev123" {
			t.Errorf("token request missing device_code form field")
		}
		n := int(env.tokenPolls.Add(1)) - 1
		if n >= len(env.tokenResponses) {
			n = len(env.tokenResponses) - 1
		}
		env.tokenResponses[n](w)
	})
	mux.HandleFunc("/api/accounts_mgmt/v1/access_token", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("access_token request Authorization = %q, want %q", got, "Bearer tok123")
		}
		w.WriteHeader(env.secretStatus)
		if env.secretStatus == http.StatusOK {
			_, _ = w.Write([]byte(fakeSecret))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	env.fetcher = NewFetcher(srv.URL, srv.URL)
	return env
}

func TestStartDeviceAuthReturnsPrompt(t *testing.T) {
	env := newTestEnv(t, tokenOK)
	prompt, err := env.fetcher.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if prompt.VerificationURI != "https://sso.example/device" || prompt.UserCode != "ABCD-EFGH" {
		t.Fatalf("prompt = %+v, want URI https://sso.example/device and code ABCD-EFGH", prompt)
	}
}

func TestStartDeviceAuthServerError(t *testing.T) {
	env := newTestEnv(t, tokenOK)
	env.deviceStatus = http.StatusInternalServerError
	if _, err := env.fetcher.StartDeviceAuth(context.Background()); err == nil {
		t.Fatal("StartDeviceAuth() = nil error, want failure on HTTP 500")
	}
}

func TestWaitAndFetchHappyPath(t *testing.T) {
	env := newTestEnv(t, tokenErr("authorization_pending"), tokenOK)
	if _, err := env.fetcher.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	data, err := env.fetcher.WaitAndFetch(context.Background())
	if err != nil {
		t.Fatalf("WaitAndFetch() error = %v", err)
	}
	if string(data) != fakeSecret {
		t.Fatalf("WaitAndFetch() = %q, want %q", data, fakeSecret)
	}
	if got := env.tokenPolls.Load(); got != 2 {
		t.Fatalf("token polls = %d, want 2 (one pending, one success)", got)
	}
}

func TestWaitAndFetchSlowDown(t *testing.T) {
	old := slowDownIncrement
	slowDownIncrement = time.Millisecond
	t.Cleanup(func() { slowDownIncrement = old })

	env := newTestEnv(t, tokenErr("slow_down"), tokenOK)
	if _, err := env.fetcher.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if _, err := env.fetcher.WaitAndFetch(context.Background()); err != nil {
		t.Fatalf("WaitAndFetch() error = %v", err)
	}
}

func TestWaitAndFetchTerminalErrors(t *testing.T) {
	for code, want := range map[string]string{
		"expired_token": "expired",
		"access_denied": "denied",
	} {
		t.Run(code, func(t *testing.T) {
			env := newTestEnv(t, tokenErr(code))
			if _, err := env.fetcher.StartDeviceAuth(context.Background()); err != nil {
				t.Fatalf("StartDeviceAuth() error = %v", err)
			}
			_, err := env.fetcher.WaitAndFetch(context.Background())
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("WaitAndFetch() = %v, want error containing %q", err, want)
			}
		})
	}
}

func TestWaitAndFetchWithoutStart(t *testing.T) {
	env := newTestEnv(t, tokenOK)
	if _, err := env.fetcher.WaitAndFetch(context.Background()); err == nil {
		t.Fatal("WaitAndFetch() before StartDeviceAuth = nil error, want failure")
	}
}

func TestWaitAndFetchAPIError(t *testing.T) {
	env := newTestEnv(t, tokenOK)
	env.secretStatus = http.StatusInternalServerError
	if _, err := env.fetcher.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	_, err := env.fetcher.WaitAndFetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("WaitAndFetch() = %v, want error mentioning HTTP 500", err)
	}
}

func TestWaitAndFetchContextCancelled(t *testing.T) {
	env := newTestEnv(t, tokenErr("authorization_pending"))
	if _, err := env.fetcher.StartDeviceAuth(context.Background()); err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := env.fetcher.WaitAndFetch(ctx); err == nil {
		t.Fatal("WaitAndFetch() with cancelled context = nil error, want failure")
	}
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./providers/redhat/ -v`
Expected: FAIL — `undefined: Fetcher` (build error).

- [ ] **Step 3: Implement the fetcher**

Create `providers/redhat/fetcher.go`:

```go
// Package redhat fetches the user's OpenShift pull secret from their Red Hat
// account: an OAuth 2.0 device authorization grant (RFC 8628) against Red
// Hat SSO, then one call to the OCM accounts API — the same API
// console.redhat.com serves the pull secret from. Self-contained per
// provider rules; stdlib HTTP only.
package redhat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TheEasyShift/easyshift/interfaces"
)

const (
	// DefaultSSORealmURL is Red Hat's external SSO realm, the issuer used by
	// ocm/rosa device-code logins.
	DefaultSSORealmURL = "https://sso.redhat.com/auth/realms/redhat-external"
	// DefaultAPIURL is the OCM API base serving the account's pull secret.
	DefaultAPIURL = "https://api.openshift.com"
	// clientID is the public OAuth client for device-code logins — the same
	// one `ocm login --use-device-code` uses. Red Hat owns it; if it ever
	// changes, callers fall back to manual `pull-secret set`.
	clientID = "ocm-cli"

	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	defaultInterval = 5 * time.Second
)

// slowDownIncrement is added to the polling interval on each slow_down
// response (RFC 8628 §3.5). Var, not const, so tests can shrink it.
var slowDownIncrement = 5 * time.Second

// Fetcher implements interfaces.PullSecretFetcher. Not safe for concurrent
// use: StartDeviceAuth stores the device-code state WaitAndFetch consumes.
type Fetcher struct {
	client      *http.Client
	ssoRealmURL string
	apiURL      string

	deviceCode string
	interval   time.Duration
	expiresAt  time.Time
}

// NewFetcher returns a Fetcher against the given SSO realm and OCM API base
// URLs (pass the Default* constants in production; tests pass an
// httptest.Server URL for both).
func NewFetcher(ssoRealmURL, apiURL string) *Fetcher {
	return &Fetcher{
		client:      &http.Client{Timeout: 30 * time.Second},
		ssoRealmURL: strings.TrimRight(ssoRealmURL, "/"),
		apiURL:      strings.TrimRight(apiURL, "/"),
	}
}

// StartDeviceAuth requests a device code and returns what to show the user.
func (f *Fetcher) StartDeviceAuth(ctx context.Context) (interfaces.DeviceAuthPrompt, error) {
	form := url.Values{"client_id": {clientID}}
	status, body, err := f.postForm(ctx, f.ssoRealmURL+"/protocol/openid-connect/auth/device", form)
	if err != nil {
		return interfaces.DeviceAuthPrompt{}, fmt.Errorf("request device code from Red Hat SSO: %w", err)
	}
	if status != http.StatusOK {
		return interfaces.DeviceAuthPrompt{}, fmt.Errorf("Red Hat SSO device endpoint returned HTTP %d: %s", status, body)
	}
	var resp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		// Pointer distinguishes an explicit 0 (poll immediately, used by
		// tests) from an absent field (RFC 8628 default of 5s).
		Interval *int `json:"interval"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return interfaces.DeviceAuthPrompt{}, fmt.Errorf("parse device code response: %w", err)
	}
	f.deviceCode = resp.DeviceCode
	f.interval = defaultInterval
	if resp.Interval != nil {
		f.interval = time.Duration(*resp.Interval) * time.Second
	}
	f.expiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	return interfaces.DeviceAuthPrompt{
		VerificationURI: resp.VerificationURI,
		UserCode:        resp.UserCode,
	}, nil
}

// WaitAndFetch polls the token endpoint until the user authorizes (or the
// code expires / ctx is cancelled), then fetches the pull secret.
func (f *Fetcher) WaitAndFetch(ctx context.Context) ([]byte, error) {
	if f.deviceCode == "" {
		return nil, fmt.Errorf("device login not started (StartDeviceAuth must be called first)")
	}
	token, err := f.pollToken(ctx)
	if err != nil {
		return nil, err
	}
	return f.fetchPullSecret(ctx, token)
}

func (f *Fetcher) pollToken(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":  {deviceGrantType},
		"device_code": {f.deviceCode},
		"client_id":   {clientID},
	}
	for {
		status, body, err := f.postForm(ctx, f.ssoRealmURL+"/protocol/openid-connect/token", form)
		if err != nil {
			return "", fmt.Errorf("poll Red Hat SSO token endpoint: %w", err)
		}
		if status == http.StatusOK {
			var tok struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
				return "", fmt.Errorf("parse token response: %v (body: %s)", err, body)
			}
			return tok.AccessToken, nil
		}
		var oauthErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oauthErr)
		switch oauthErr.Error {
		case "authorization_pending":
			// keep polling
		case "slow_down":
			f.interval += slowDownIncrement
		case "expired_token":
			return "", fmt.Errorf("the device code expired before the login was authorized; run the command again")
		case "access_denied":
			return "", fmt.Errorf("the login request was denied")
		default:
			return "", fmt.Errorf("Red Hat SSO token endpoint returned HTTP %d: %s", status, body)
		}
		if time.Now().After(f.expiresAt) {
			return "", fmt.Errorf("the device code expired before the login was authorized; run the command again")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(f.interval):
		}
	}
}

func (f *Fetcher) fetchPullSecret(ctx context.Context, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.apiURL+"/api/accounts_mgmt/v1/access_token", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch pull secret from %s: %w", f.apiURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read pull secret response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("OCM pull secret endpoint returned HTTP %d: %s", resp.StatusCode, body)
	}
	return body, nil
}

// postForm posts form data and returns the status code and full body. Any
// status is returned to the caller (token polling treats 400s as signals).
func (f *Fetcher) postForm(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./providers/redhat/ -v`
Expected: PASS, all 8 test functions, in well under 5 seconds (no real sleeps: interval is 0, slow_down increment shrunk to 1ms).

- [ ] **Step 5: Commit**

```bash
git add providers/redhat/
git commit -s -m "providers: add redhat pull-secret fetcher via SSO device flow

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 4: Fake fetcher + production wiring

**Files:**
- Modify: `providers/fakes/fakes.go` (new fake; wire into `All()` at line ~668 and `Bundle` at line ~816)
- Modify: `app/deps.go`

- [ ] **Step 1: Add the fake to `providers/fakes/fakes.go`**

Insert above the `All()` function (line ~666):

```go
// PullSecretFetcher is a fake interfaces.PullSecretFetcher. It hands back a
// canned prompt and a syntactically valid pull secret unless errors are set.
type PullSecretFetcher struct {
	StartErr error
	FetchErr error
	// Secret overrides the default fake pull secret returned by WaitAndFetch.
	Secret      []byte
	StartCalled bool
	FetchCalled bool
}

// StartDeviceAuth records the call and returns a canned prompt or StartErr.
func (p *PullSecretFetcher) StartDeviceAuth(_ context.Context) (interfaces.DeviceAuthPrompt, error) {
	p.StartCalled = true
	if p.StartErr != nil {
		return interfaces.DeviceAuthPrompt{}, p.StartErr
	}
	return interfaces.DeviceAuthPrompt{VerificationURI: "https://fake.sso.example/device", UserCode: "FAKE-CODE"}, nil
}

// WaitAndFetch records the call and returns Secret (or a valid stand-in) or FetchErr.
func (p *PullSecretFetcher) WaitAndFetch(_ context.Context) ([]byte, error) {
	p.FetchCalled = true
	if p.FetchErr != nil {
		return nil, p.FetchErr
	}
	if len(p.Secret) == 0 {
		return []byte(`{"auths":{"fake.registry":{"auth":"ZmFrZQ=="}}}`), nil
	}
	return p.Secret, nil
}
```

- [ ] **Step 2: Wire into `All()` and `Bundle`**

In `All()`, add to the `&Bundle{...}` literal (after `CertIssuer: &CertIssuer{},`):

```go
		PullSecret: &PullSecretFetcher{},
```

In the returned `interfaces.Deps{...}` literal (after `DNSManager: b.DNSManager,`):

```go
		PullSecret: b.PullSecret,
```

In the `Bundle` struct (after `CertIssuer *CertIssuer`):

```go
	PullSecret *PullSecretFetcher
```

- [ ] **Step 3: Wire the real fetcher in `app/deps.go`**

Add to the import block:

```go
	"github.com/TheEasyShift/easyshift/providers/redhat"
```

In the `interfaces.Deps{...}` literal in `NewProductionDeps`, after `DNSManager: newProductionDNSManager(cfg),`:

```go
		PullSecret: redhat.NewFetcher(redhat.DefaultSSORealmURL, redhat.DefaultAPIURL),
```

- [ ] **Step 4: Verify build + full test suite**

Run: `go build ./... && go test ./...`
Expected: success; all existing tests still pass (the new Deps field is additive).

- [ ] **Step 5: Commit**

```bash
git add providers/fakes/fakes.go app/deps.go
git commit -s -m "app: wire the redhat pull-secret fetcher and its fake into deps

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 5: cmd interactive flow + `pull-secret login` + create hook

All interactivity lives in `cmd/easyshift`. The flow helpers take `io.Reader`/`io.Writer`/`isTTY` parameters so tests never touch a real terminal.

Note on `**config.Config`: in `--simulate` mode `PersistentPreRunE` *reassigns* the local `cfg` variable to a throwaway config dir (`cmd/easyshift/main.go:59`). Commands built before that run must therefore receive `&cfg` (pointer-to-pointer), same pattern as `&mgr`. This task switches `newPullSecretCommand` to that pattern too (it currently captures the stale pre-simulate `cfg`).

**Files:**
- Create: `cmd/easyshift/pullsecret_flow.go`
- Test: `cmd/easyshift/pullsecret_flow_test.go`
- Modify: `cmd/easyshift/main.go` (`newCreateCommand` line ~120, `newPullSecretCommand` line ~340, registrations lines ~104 and ~111)
- Modify: `config/pullsecret.go:23` (error message)

- [ ] **Step 1: Write the failing tests**

Create `cmd/easyshift/pullsecret_flow_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	return config.NewDefaultConfig(t.TempDir())
}

func TestEnsurePullSecretAlreadyConfigured(t *testing.T) {
	cfg := testCfg(t)
	if err := config.WritePullSecret(cfg.ConfigDir, []byte(`{"auths":{"q":{"auth":"eA=="}}}`)); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakes.PullSecretFetcher{}
	var out bytes.Buffer
	if err := ensurePullSecret(context.Background(), cfg, fetcher, strings.NewReader(""), &out, true); err != nil {
		t.Fatalf("ensurePullSecret() = %v, want nil", err)
	}
	if fetcher.StartCalled {
		t.Fatal("fetcher was invoked although a pull secret already exists")
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output for already-configured case: %q", out.String())
	}
}

func TestEnsurePullSecretNonTTYFailsWithGuidance(t *testing.T) {
	cfg := testCfg(t)
	fetcher := &fakes.PullSecretFetcher{}
	var out bytes.Buffer
	err := ensurePullSecret(context.Background(), cfg, fetcher, strings.NewReader(""), &out, false)
	if err == nil || !strings.Contains(err.Error(), pullSecretConsoleURL) {
		t.Fatalf("ensurePullSecret() = %v, want error containing %q", err, pullSecretConsoleURL)
	}
	if fetcher.StartCalled {
		t.Fatal("fetcher must not run without a TTY")
	}
}

func TestEnsurePullSecretAcceptFetchesAndStores(t *testing.T) {
	cfg := testCfg(t)
	fetcher := &fakes.PullSecretFetcher{}
	var out bytes.Buffer
	// Bare Enter accepts the default (Y).
	if err := ensurePullSecret(context.Background(), cfg, fetcher, strings.NewReader("\n"), &out, true); err != nil {
		t.Fatalf("ensurePullSecret() = %v, want nil", err)
	}
	if !fetcher.FetchCalled {
		t.Fatal("fetcher was not invoked on accept")
	}
	if err := config.ValidatePullSecretJSON(cfg.ConfigDir); err != nil {
		t.Fatalf("stored pull secret invalid: %v", err)
	}
	for _, want := range []string{"FAKE-CODE", "https://fake.sso.example/device"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q missing %q", out.String(), want)
		}
	}
}

func TestEnsurePullSecretDeclinePrintsManualPath(t *testing.T) {
	cfg := testCfg(t)
	fetcher := &fakes.PullSecretFetcher{}
	var out bytes.Buffer
	err := ensurePullSecret(context.Background(), cfg, fetcher, strings.NewReader("n\n"), &out, true)
	if err == nil || !strings.Contains(err.Error(), "pull-secret set") {
		t.Fatalf("ensurePullSecret() = %v, want error pointing at pull-secret set", err)
	}
	if fetcher.StartCalled {
		t.Fatal("fetcher must not run when the user declines")
	}
	if _, statErr := os.Stat(config.PullSecretPath(cfg.ConfigDir)); statErr == nil {
		t.Fatal("pull secret file must not exist after decline")
	}
}

func TestFetchAndStoreRejectsInvalidSecret(t *testing.T) {
	cfg := testCfg(t)
	fetcher := &fakes.PullSecretFetcher{Secret: []byte("not-json")}
	var out bytes.Buffer
	err := fetchAndStorePullSecret(context.Background(), cfg, fetcher, &out)
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("fetchAndStorePullSecret() = %v, want JSON validation error", err)
	}
	if _, statErr := os.Stat(config.PullSecretPath(cfg.ConfigDir)); statErr == nil {
		t.Fatal("invalid pull secret must not be written to disk")
	}
}

func TestFetchAndStoreWrapsFetchErrorWithFallback(t *testing.T) {
	cfg := testCfg(t)
	fetcher := &fakes.PullSecretFetcher{FetchErr: errors.New("the login request was denied")}
	var out bytes.Buffer
	err := fetchAndStorePullSecret(context.Background(), cfg, fetcher, &out)
	if err == nil || !strings.Contains(err.Error(), pullSecretConsoleURL) {
		t.Fatalf("fetchAndStorePullSecret() = %v, want error mentioning the manual fallback URL", err)
	}
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./cmd/easyshift/ -v`
Expected: FAIL — `undefined: ensurePullSecret`, `undefined: fetchAndStorePullSecret`.

- [ ] **Step 3: Implement the flow**

Create `cmd/easyshift/pullsecret_flow.go`:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
)

const pullSecretConsoleURL = "https://console.redhat.com/openshift/install/pull-secret"

// pullSecretExplanation is the guidance shown whenever no pull secret is
// configured: what it is and the two ways to set one up.
func pullSecretExplanation() string {
	return fmt.Sprintf(`No pull secret configured.

A pull secret is a free credential from your Red Hat account that lets the
installer download OpenShift container images. Two ways to set it up:

  1. Log in to your Red Hat account now (recommended) — you'll get a short
     code to enter at a Red Hat URL from any browser, e.g. your laptop.
  2. Download it yourself from
     %s
     and run: easyshift pull-secret set <file>
`, pullSecretConsoleURL)
}

// stdinIsTTY reports whether stdin is an interactive terminal.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ensurePullSecret guarantees a pull secret exists before create proceeds.
// Interactive terminals get an offer to fetch one via Red Hat SSO;
// non-interactive runs fail immediately with the manual instructions.
func ensurePullSecret(ctx context.Context, cfg *config.Config, fetcher interfaces.PullSecretFetcher, in io.Reader, out io.Writer, isTTY bool) error {
	if config.EnsurePullSecret(cfg.ConfigDir) == nil {
		return nil
	}
	if !isTTY {
		return fmt.Errorf("%s\nthen re-run this command (or run `easyshift pull-secret login` from an interactive terminal)", pullSecretExplanation())
	}
	fmt.Fprint(out, pullSecretExplanation())
	fmt.Fprint(out, "\nLog in and fetch it now? [Y/n] ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "" && answer != "y" && answer != "yes" {
		return fmt.Errorf("pull secret not configured: download it from %s and run `easyshift pull-secret set <file>`", pullSecretConsoleURL)
	}
	return fetchAndStorePullSecret(ctx, cfg, fetcher, out)
}

// fetchAndStorePullSecret runs the device-code login, validates the fetched
// secret, and persists it. Every failure path names the manual fallback.
func fetchAndStorePullSecret(ctx context.Context, cfg *config.Config, fetcher interfaces.PullSecretFetcher, out io.Writer) error {
	manualFallback := fmt.Sprintf("fallback: download the pull secret from %s and run `easyshift pull-secret set <file>`", pullSecretConsoleURL)
	prompt, err := fetcher.StartDeviceAuth(ctx)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, manualFallback)
	}
	fmt.Fprintf(out, "\n  On any device, open:  %s\n  and enter the code:   %s\n\nWaiting for authorization...\n", prompt.VerificationURI, prompt.UserCode)
	data, err := fetcher.WaitAndFetch(ctx)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, manualFallback)
	}
	if err := config.ValidatePullSecretBytes(data); err != nil {
		return fmt.Errorf("Red Hat returned an unusable pull secret: %w\n\n%s", err, manualFallback)
	}
	if err := config.WritePullSecret(cfg.ConfigDir, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "Pull secret stored at %s\n", config.PullSecretPath(cfg.ConfigDir))
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./cmd/easyshift/ -v`
Expected: PASS, all 6 test functions.

- [ ] **Step 5: Hook into `create` and add `pull-secret login`**

All in `cmd/easyshift/main.go`:

a) Change `newCreateCommand`'s signature (line ~120) to:

```go
func newCreateCommand(mgr **app.ClusterManager, simBundle **fakes.Bundle, cfgp **config.Config, depsp *interfaces.Deps) *cobra.Command {
```

b) In its `RunE`, immediately before `return (*mgr).Create(context.Background(), c)`:

```go
			if err := ensurePullSecret(context.Background(), *cfgp, depsp.PullSecret, os.Stdin, os.Stdout, stdinIsTTY()); err != nil {
				return err
			}
```

c) Change `newPullSecretCommand`'s signature (line ~340) to:

```go
func newPullSecretCommand(cfgp **config.Config, depsp *interfaces.Deps) *cobra.Command {
```

Inside it, replace every use of `cfg` with `(*cfgp)` — three sites: `config.WritePullSecret(cfg.ConfigDir, data)`, `config.PullSecretPath(cfg.ConfigDir)` in `set`, and `config.EnsurePullSecret(cfg.ConfigDir)` + `config.PullSecretPath(cfg.ConfigDir)` in `show`. (This also fixes `set`/`show` reading the stale pre-`--simulate` config pointer.)

d) Add a `login` subcommand inside `newPullSecretCommand`, after the `show` subcommand:

```go
	cmd.AddCommand(&cobra.Command{
		Use:   "login",
		Short: "Fetch the pull secret from your Red Hat account (device-code login)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fetchAndStorePullSecret(context.Background(), *cfgp, depsp.PullSecret, os.Stdout)
		},
	})
```

e) Update the registrations (lines ~104 and ~111):

```go
		newCreateCommand(&mgr, &simBundle, &cfg, &deps),
```

```go
		newPullSecretCommand(&cfg, &deps),
```

f) In `config/pullsecret.go:23`, update the backstop error message to name the new command:

```go
			return fmt.Errorf("pull secret not configured: run `easyshift pull-secret login` (or `easyshift pull-secret set <file>`) first (expected at %s)", path)
```

Then run `grep -rn 'pull secret not configured' --include='*_test.go' .` — if any test asserts the old message text, update it to match.

- [ ] **Step 6: Verify build, full tests, and a smoke run**

Run: `go build ./... && go test ./...`
Expected: all packages pass.

Run: `make build && echo "" | ./easyshift create --name smoketest 2>&1 | head -20`
Expected (on a machine without a pull secret AND with stdin piped, i.e. non-TTY): the guidance block with the console.redhat.com URL and a non-zero exit — and **no** polling/hang. On a machine that has a real pull secret configured this proves nothing; in that case verify with `./easyshift pull-secret login --help` showing the new subcommand and skip the create smoke.

- [ ] **Step 7: Commit**

```bash
git add cmd/easyshift/ config/pullsecret.go
git commit -s -m "cmd: offer Red Hat SSO pull-secret fetch in create and add login

Assisted-by: Claude Code/claude-fable-5"
```

---

### Task 6: Docs + final check

**Files:**
- Modify: `docs/user/configuration.md` (Pull secret section, line ~42)
- Modify: `README.md` (requirements table line 24, quickstart lines 41-42)

- [ ] **Step 1: Update `docs/user/configuration.md`**

Replace the `## Pull secret` section's code block and intro:

```markdown
## Pull secret

Required before any `create`. Stored once at `~/.config/easyshift/pull-secret`
(mode `0600`); it is **not** kept in `config.json`.

The easiest path is a device-code login: easyshift prints a short code and a
Red Hat URL; you authorize from any browser (e.g. your laptop — handy when
easyshift runs on a headless box) and the pull secret is fetched and stored
automatically. `create` offers this interactively when no pull secret is
configured. No SSO token is persisted — only the pull secret itself.

```sh
easyshift pull-secret login                             # fetch via Red Hat account login
easyshift pull-secret set ~/Downloads/pull-secret.txt   # from a file
easyshift pull-secret set -                             # from stdin
easyshift pull-secret show                              # print the stored path
```

The manual `set` path (download from
<https://console.redhat.com/openshift/install/pull-secret>) remains for
air-gapped hosts or if the Red Hat SSO flow is unavailable.
```

- [ ] **Step 2: Update `README.md`**

Line 24, requirements table row — replace with:

```markdown
| OpenShift pull secret | Fetched via `easyshift pull-secret login` (or download from <https://console.redhat.com/openshift/install/pull-secret>) |
```

Lines 41-42, quickstart — replace with:

```sh
# 1. Store your pull secret once (log in to your Red Hat account; or use
#    `easyshift pull-secret set <file>` with a downloaded secret).
easyshift pull-secret login
```

- [ ] **Step 3: Run the full check**

Run: `make check`
Expected: lint + build + test all pass. Fix any gofmt/vet/golangci-lint findings before committing.

- [ ] **Step 4: Commit**

```bash
git add docs/user/configuration.md README.md
git commit -s -m "docs: document pull-secret login and the interactive create flow

Assisted-by: Claude Code/claude-fable-5"
```
