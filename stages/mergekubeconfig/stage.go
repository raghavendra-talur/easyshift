// Package mergekubeconfig merges the cluster's admin credentials into the
// user's kubeconfig (~/.kube/config or $KUBECONFIG's first path) as a context
// named after the cluster, and makes it the current context — minikube-style
// "kubectl works the moment create returns". Rollback removes exactly the
// entries Apply created, so delete cleans up for free.
package mergekubeconfig

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/TheEasyShift/easyshift/interfaces"
)

// Stage merges/removes the cluster's kubeconfig context.
type Stage struct {
	cmd interfaces.CommandRunner
}

// New returns the merge-kubeconfig stage.
func New(cmd interfaces.CommandRunner) *Stage { return &Stage{cmd: cmd} }

func (*Stage) Name() string { return "merge-kubeconfig" }

// targetKubeconfig resolves the user's kubeconfig: the first $KUBECONFIG
// path if set, else ~/.kube/config.
func targetKubeconfig() (string, error) {
	if env := os.Getenv("KUBECONFIG"); env != "" {
		if paths := filepath.SplitList(env); len(paths) > 0 && paths[0] != "" {
			return paths[0], nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".kube", "config"), nil
}

func clusterEntry(name string) string { return "easyshift-" + name }
func userEntry(name string) string    { return "easyshift-" + name + "-admin" }

// Apply extracts the admin client cert/key (and CA bundle, when the admin
// kubeconfig still carries one) and writes set-cluster/set-credentials/
// set-context/use-context entries into the user's kubeconfig. Every step is
// an idempotent `oc config set-*`, so retries are safe. A pre-existing
// foreign context with the cluster's name is overwritten (documented).
func (s *Stage) Apply(ctx context.Context, sc *interfaces.StageContext) error {
	target, err := targetKubeconfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create kubeconfig dir: %w", err)
	}
	sc.Cluster.KubeconfigTarget = target
	if err := sc.Config.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	oc := sc.OCBinaryPath()
	admin := sc.KubeconfigPath()
	name := sc.Cluster.Name
	authDir := filepath.Join(sc.ClusterDir(), "auth")

	server, err := s.jsonpath(ctx, oc, admin, "{.clusters[0].cluster.server}")
	if err != nil {
		return err
	}
	caData, err := s.jsonpath(ctx, oc, admin, "{.clusters[0].cluster.certificate-authority-data}")
	if err != nil {
		return err
	}
	certData, err := s.jsonpath(ctx, oc, admin, "{.users[0].user.client-certificate-data}")
	if err != nil {
		return err
	}
	keyData, err := s.jsonpath(ctx, oc, admin, "{.users[0].user.client-key-data}")
	if err != nil {
		return err
	}

	if certData == "" || keyData == "" {
		logrus.Warnf("merge-kubeconfig: admin kubeconfig %s has no embedded client cert/key; "+
			"the merged context may not authenticate", admin)
	}

	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	certPath := filepath.Join(authDir, "client.crt")
	keyPath := filepath.Join(authDir, "client.key")
	if err := writeB64(certPath, certData); err != nil {
		return err
	}
	if err := writeB64(keyPath, keyData); err != nil {
		return err
	}

	setCluster := []string{"--kubeconfig", target, "config", "set-cluster", clusterEntry(name), "--server=" + server}
	if caData != "" {
		caPath := filepath.Join(authDir, "ca-bundle.crt")
		if err := writeB64(caPath, caData); err != nil {
			return err
		}
		setCluster = append(setCluster, "--certificate-authority="+caPath, "--embed-certs=true")
	}
	// No CA data (Let's Encrypt cluster): the entry validates via the system
	// trust store, so no CA flags at all.
	steps := [][]string{
		setCluster,
		{"--kubeconfig", target, "config", "set-credentials", userEntry(name),
			"--client-certificate=" + certPath, "--client-key=" + keyPath, "--embed-certs=true"},
		{"--kubeconfig", target, "config", "set-context", name,
			"--cluster=" + clusterEntry(name), "--user=" + userEntry(name)},
		{"--kubeconfig", target, "config", "use-context", name},
	}
	for _, args := range steps {
		if _, err := s.cmd.Run(ctx, oc, args...); err != nil {
			return fmt.Errorf("oc %s: %w", strings.Join(args[2:4], " "), err)
		}
	}
	logrus.Infof("merged context %q into %s and set it current", name, target)
	return nil
}

// Rollback removes the context/cluster/user entries Apply created and
// unsets current-context only if it still points at our context. Best
// effort by design: a missing kubeconfig or entry must not block delete.
func (s *Stage) Rollback(ctx context.Context, sc *interfaces.StageContext) error {
	target := sc.Cluster.KubeconfigTarget
	if target == "" {
		var err error
		target, err = targetKubeconfig()
		if err != nil {
			logrus.Warnf("merge-kubeconfig rollback: %v", err)
			return nil
		}
	}
	oc := sc.OCBinaryPath()
	name := sc.Cluster.Name

	if out, err := s.cmd.Run(ctx, oc, "--kubeconfig", target, "config", "current-context"); err == nil &&
		strings.TrimSpace(string(out)) == name {
		if _, err := s.cmd.Run(ctx, oc, "--kubeconfig", target, "config", "unset", "current-context"); err != nil {
			logrus.Warnf("merge-kubeconfig rollback: unset current-context: %v", err)
		}
	}
	for _, args := range [][]string{
		{"--kubeconfig", target, "config", "delete-context", name},
		{"--kubeconfig", target, "config", "delete-cluster", clusterEntry(name)},
		{"--kubeconfig", target, "config", "unset", "users." + userEntry(name)},
	} {
		if _, err := s.cmd.Run(ctx, oc, args...); err != nil {
			logrus.Warnf("merge-kubeconfig rollback: oc %v: %v", args, err)
		}
	}
	sc.Cluster.KubeconfigTarget = ""
	if err := sc.Config.Save(); err != nil {
		logrus.Warnf("merge-kubeconfig rollback: save config: %v", err)
	}
	return nil
}

// jsonpath runs `oc config view --raw -o jsonpath=<expr>` against kubeconfig
// and returns the trimmed output.
func (s *Stage) jsonpath(ctx context.Context, oc, kubeconfig, expr string) (string, error) {
	out, err := s.cmd.Run(ctx, oc, "--kubeconfig", kubeconfig, "config", "view", "--raw", "-o", "jsonpath="+expr)
	if err != nil {
		return "", fmt.Errorf("extract %s from admin kubeconfig: %w", expr, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeB64 decodes base64 data (possibly empty under --simulate) to path.
func writeB64(path, b64 string) error {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("decode for %s: %w", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
