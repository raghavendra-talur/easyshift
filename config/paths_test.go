package config

import (
	"path/filepath"
	"testing"
)

func TestLocalCAPaths(t *testing.T) {
	dir := "/home/u/.config/easyshift"
	if got, want := LocalCADir(dir), filepath.Join(dir, "ca"); got != want {
		t.Errorf("LocalCADir = %q, want %q", got, want)
	}
	if got, want := LocalCACertPath(dir), filepath.Join(dir, "ca", "ca.crt"); got != want {
		t.Errorf("LocalCACertPath = %q, want %q", got, want)
	}
	if got, want := LocalCATrustedMarkerPath(dir), filepath.Join(dir, "ca", "trusted"); got != want {
		t.Errorf("LocalCATrustedMarkerPath = %q, want %q", got, want)
	}
}
