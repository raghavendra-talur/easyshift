package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PullSecretPath returns the on-disk location of the persisted pull secret.
// The secret lives outside config.json so it never accidentally ends up in
// shell history, screenshots, or backup tooling that grabs the main config.
func PullSecretPath(configDir string) string {
	return filepath.Join(configDir, DefaultPullSecret)
}

// EnsurePullSecret returns nil iff a pull secret has been configured.
// The error message tells the user exactly how to fix the missing setup.
func EnsurePullSecret(configDir string) error {
	path := PullSecretPath(configDir)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("pull secret not configured: run `easyshift pull-secret login` (or `easyshift pull-secret set <file>`) first (expected at %s)", path)
		}
		return fmt.Errorf("stat pull secret %s: %w", path, err)
	}
	return nil
}

// ReadPullSecret reads the persisted pull secret and trims surrounding whitespace.
func ReadPullSecret(configDir string) (string, error) {
	data, err := os.ReadFile(PullSecretPath(configDir))
	if err != nil {
		return "", fmt.Errorf("read pull secret: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// WritePullSecret stores data as the pull secret with 0600 permissions,
// creating configDir if it does not exist.
func WritePullSecret(configDir string, data []byte) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(PullSecretPath(configDir), data, 0o600)
}
