package easyshift

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// writeFile writes data to a file with proper permissions
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0600)
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Open source file
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer in.Close()

	// Create destination file
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	// Copy contents
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	return nil
}

// checkCommand checks if a command exists in PATH
func checkCommand(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// checkRequiredCommands checks if all required commands are available
func checkRequiredCommands() error {
	requiredCmds := []string{
		"virsh",
		"virt-install",
		"ssh-keygen",
		"tar",
	}

	for _, cmd := range requiredCmds {
		if !checkCommand(cmd) {
			return fmt.Errorf("required command not found: %s", cmd)
		}
	}

	return nil
}

// isRoot checks if the current user is root
func isRoot() bool {
	return os.Geteuid() == 0
}

// ensureDirectory ensures a directory exists with proper permissions
func ensureDirectory(path string) error {
	return os.MkdirAll(path, 0700)
}

// removeIfExists removes a file or directory if it exists
func removeIfExists(path string) error {
	if _, err := os.Stat(path); err == nil {
		return os.RemoveAll(path)
	}
	return nil
}

// validateClusterName validates a cluster name
func validateClusterName(name string) error {
	if name == "" {
		return fmt.Errorf("cluster name cannot be empty")
	}
	// Add more validation as needed
	return nil
}

// getClusterDir returns the directory path for a cluster
func getClusterDir(baseDir, clusterName string) string {
	return filepath.Join(baseDir, "clusters", clusterName)
}
