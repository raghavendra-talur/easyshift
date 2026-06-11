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
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
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
