package easyshift

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/sirupsen/logrus"
)

const (
	installConfigTemplate = `apiVersion: v1
baseDomain: {{.Domain}}
compute:
- hyperthreading: Enabled
  name: worker
  replicas: {{.WorkerCount}}
controlPlane:
  hyperthreading: Enabled
  name: master
  replicas: {{.MasterCount}}
metadata:
  name: {{.Name}}
networking:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  networkType: OVNKubernetes
  serviceNetwork:
  - 172.30.0.0/16
platform:
  none: {}
pullSecret: '{{.PullSecret}}'
sshKey: '{{.SSHKey}}'`
)

// InstallManager handles OpenShift installation
type InstallManager struct {
	baseDir    string
	httpServer *HTTPServer
}

// NewInstallManager creates a new install manager
func NewInstallManager(baseDir string, httpServer *HTTPServer) *InstallManager {
	return &InstallManager{
		baseDir:    baseDir,
		httpServer: httpServer,
	}
}

// PrepareInstallation prepares for OpenShift installation
func (im *InstallManager) PrepareInstallation(cluster *ClusterConfig) error {
	clusterDir := filepath.Join(im.baseDir, "clusters", cluster.Name)

	// Create necessary directories
	dirs := []string{
		clusterDir,
		filepath.Join(clusterDir, "auth"),
		filepath.Join(clusterDir, "downloads"),
		filepath.Join(clusterDir, "vms"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Download OpenShift binaries
	if err := im.downloadBinaries(cluster); err != nil {
		return fmt.Errorf("failed to download binaries: %w", err)
	}

	// Generate SSH key if not exists
	if err := im.generateSSHKey(cluster); err != nil {
		return fmt.Errorf("failed to generate SSH key: %w", err)
	}

	// Create install config
	if err := im.createInstallConfig(cluster); err != nil {
		return fmt.Errorf("failed to create install config: %w", err)
	}

	// Generate ignition configs
	if err := im.generateIgnitionConfigs(cluster); err != nil {
		return fmt.Errorf("failed to generate ignition configs: %w", err)
	}

	return nil
}

func (im *InstallManager) downloadBinaries(cluster *ClusterConfig) error {
	downloadsDir := filepath.Join(im.baseDir, "clusters", cluster.Name, "downloads")

	// Download OpenShift installer
	installerURL := fmt.Sprintf("%s/clients/ocp/%s/openshift-install-linux.tar.gz", OCPMirrorURL, cluster.OCPVersion)
	if err := im.downloadAndExtract(installerURL, downloadsDir); err != nil {
		return fmt.Errorf("failed to download installer: %w", err)
	}

	// Download OpenShift client
	clientURL := fmt.Sprintf("%s/clients/ocp/%s/openshift-client-linux.tar.gz", OCPMirrorURL, cluster.OCPVersion)
	if err := im.downloadAndExtract(clientURL, downloadsDir); err != nil {
		return fmt.Errorf("failed to download client: %w", err)
	}

	return nil
}

func (im *InstallManager) downloadAndExtract(url, destDir string) error {
	logrus.Infof("Downloading from %s", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download %s: %w", url, err)
	}
	defer resp.Body.Close()

	tmpFile := filepath.Join(destDir, "download.tar.gz")
	out, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("failed to write download: %w", err)
	}

	// Extract archive
	cmd := exec.Command("tar", "xzf", tmpFile, "-C", destDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	// Cleanup
	os.Remove(tmpFile)
	return nil
}

func (im *InstallManager) generateSSHKey(cluster *ClusterConfig) error {
	sshKeyPath := filepath.Join(im.baseDir, "clusters", cluster.Name, "ssh_key")
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "4096", "-f", sshKeyPath, "-N", "")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to generate SSH key: %w", err)
		}
	}
	return nil
}

func (im *InstallManager) createInstallConfig(cluster *ClusterConfig) error {
	configPath := filepath.Join(im.baseDir, "clusters", cluster.Name, "install-config.yaml")

	// Read SSH public key
	sshKeyPath := filepath.Join(im.baseDir, "clusters", cluster.Name, "ssh_key.pub")
	sshKeyBytes, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH public key: %w", err)
	}

	// Create template data
	data := struct {
		*ClusterConfig
		SSHKey string
	}{
		ClusterConfig: cluster,
		SSHKey:        string(sshKeyBytes),
	}

	// Parse and execute template
	tmpl, err := template.New("install-config").Parse(installConfigTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse install config template: %w", err)
	}

	f, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("failed to create install config file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("failed to write install config: %w", err)
	}

	return nil
}

func (im *InstallManager) generateIgnitionConfigs(cluster *ClusterConfig) error {
	clusterDir := filepath.Join(im.baseDir, "clusters", cluster.Name)

	// Create manifests
	cmd := exec.Command(
		filepath.Join(clusterDir, "downloads/openshift-install"),
		"create", "manifests",
		"--dir", clusterDir,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create manifests: %w", err)
	}

	// Create ignition configs
	cmd = exec.Command(
		filepath.Join(clusterDir, "downloads/openshift-install"),
		"create", "ignition-configs",
		"--dir", clusterDir,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create ignition configs: %w", err)
	}

	// Copy ignition files to HTTP server directory
	ignitionFiles := []string{"bootstrap.ign", "master.ign", "worker.ign"}
	for _, file := range ignitionFiles {
		src := filepath.Join(clusterDir, file)
		dst := filepath.Join(im.httpServer.RootDir, cluster.Name, file)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("failed to copy ignition file %s: %w", file, err)
		}
	}

	return nil
}

// MonitorInstallation monitors the OpenShift installation progress
func (im *InstallManager) MonitorInstallation(cluster *ClusterConfig) error {
	clusterDir := filepath.Join(im.baseDir, "clusters", cluster.Name)

	cmd := exec.Command(
		filepath.Join(clusterDir, "downloads/openshift-install"),
		"wait-for", "install-complete",
		"--dir", clusterDir,
		"--log-level", "info",
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
