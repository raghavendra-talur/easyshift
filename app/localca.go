package app

import (
	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/providers/localca"
)

// EnsureLocalCA generates the host-local CA if missing and returns the CA
// cert path. Exposed here so cmd (the `trust` command) never imports a
// concrete provider.
func EnsureLocalCA(cfg *config.Config) (string, error) {
	return localca.EnsureCA(config.LocalCADir(cfg.ConfigDir))
}
