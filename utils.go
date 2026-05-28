package easyshift

import (
	"os"
	"os/exec"
)

// IsRoot reports whether the current process is running as root.
func IsRoot() bool { return os.Geteuid() == 0 }

// CommandExists reports whether name is found on PATH.
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// RequiredCommands is the set of host binaries easyshift shells out to.
// Callers should verify availability before invoking commands that need them.
var RequiredCommands = []string{
	"virsh",
	"virt-install",
	"ssh-keygen",
	"tar",
}
