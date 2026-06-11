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
