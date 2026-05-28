package config

import "os"

// MkdirAllForTest is an internal helper exposed for tests that need to ensure
// a config directory exists before calling Config.Save. Production code should
// use LoadConfig which handles directory creation itself.
func MkdirAllForTest(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
