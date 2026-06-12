package mergekubeconfig

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/config"
	"github.com/TheEasyShift/easyshift/interfaces"
	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func newEnv(t *testing.T) (*Stage, *interfaces.StageContext, *fakes.CommandRunner, string) {
	t.Helper()
	tmp := t.TempDir()
	target := filepath.Join(tmp, "kube", "config")
	t.Setenv("KUBECONFIG", target)

	cfg := config.NewDefaultConfig(tmp)
	c := &config.ClusterConfig{Name: "dr1", Domain: "example.test", OCPVersion: "4.99.0", MasterCount: 1}
	cfg.Clusters = []*config.ClusterConfig{c}
	sc := &interfaces.StageContext{Cluster: c, Config: cfg}
	if err := os.MkdirAll(filepath.Join(sc.ClusterDir(), "auth"), 0o700); err != nil {
		t.Fatal(err)
	}

	cmd := &fakes.CommandRunner{}
	cmd.RunFunc = func(_ string, args []string) ([]byte, error) {
		j := strings.Join(args, " ")
		switch {
		case strings.Contains(j, ".cluster.server"):
			return []byte("https://api.dr1.example.test:6443"), nil
		case strings.Contains(j, "certificate-authority-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CABUNDLE"))), nil
		case strings.Contains(j, "client-certificate-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CLIENTCRT"))), nil
		case strings.Contains(j, "client-key-data"):
			return []byte(base64.StdEncoding.EncodeToString([]byte("CLIENTKEY"))), nil
		case strings.Contains(j, "current-context"):
			return []byte("dr1\n"), nil
		}
		return nil, nil
	}
	return New(cmd), sc, cmd, target
}

func joined(cmd *fakes.CommandRunner) []string {
	var out []string
	for _, c := range cmd.Calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

func TestApply_MergesContextAndSetsCurrent(t *testing.T) {
	s, sc, cmd, target := newEnv(t)

	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Client material extracted and decoded to files for --embed-certs.
	crt, err := os.ReadFile(filepath.Join(sc.ClusterDir(), "auth", "client.crt"))
	if err != nil || string(crt) != "CLIENTCRT" {
		t.Errorf("client.crt = %q, %v", crt, err)
	}

	calls := joined(cmd)
	wants := []string{
		"--kubeconfig " + target + " config set-cluster easyshift-dr1 --server=https://api.dr1.example.test:6443",
		"--certificate-authority=",
		"--embed-certs=true",
		"config set-credentials easyshift-dr1-admin",
		"config set-context dr1 --cluster=easyshift-dr1 --user=easyshift-dr1-admin",
		"config use-context dr1",
	}
	for _, want := range wants {
		found := false
		for _, c := range calls {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call containing %q in %v", want, calls)
		}
	}

	// Target parent dir created.
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Errorf("target dir not created: %v", err)
	}

	// KubeconfigTarget must be recorded on the cluster for rollback.
	if sc.Cluster.KubeconfigTarget != target {
		t.Errorf("KubeconfigTarget: got %q, want %q", sc.Cluster.KubeconfigTarget, target)
	}
}

func TestRollback_RemovesEntriesAndUnsetsCurrentContext(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)

	if err := s.Rollback(context.Background(), sc); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	calls := joined(cmd)
	wants := []string{
		"config delete-context dr1",
		"config delete-cluster easyshift-dr1",
		"config unset users.easyshift-dr1-admin",
		"config unset current-context", // current-context was "dr1" per the fake
	}
	for _, want := range wants {
		found := false
		for _, c := range calls {
			if strings.Contains(c, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call containing %q in %v", want, calls)
		}
	}
}

func TestRollback_KeepsForeignCurrentContext(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)
	inner := cmd.RunFunc
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "current-context") &&
			!strings.Contains(strings.Join(args, " "), "unset") {
			return []byte("someone-elses-context\n"), nil
		}
		return inner(name, args)
	}

	if err := s.Rollback(context.Background(), sc); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	for _, c := range joined(cmd) {
		if strings.Contains(c, "unset current-context") {
			t.Errorf("must not unset a current-context easyshift doesn't own: %v", c)
		}
	}
}

func TestRollback_UsesRecordedTarget(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)
	recorded := filepath.Join(t.TempDir(), "recorded-kubeconfig")
	sc.Cluster.KubeconfigTarget = recorded
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "wrong-kubeconfig"))

	if err := s.Rollback(context.Background(), sc); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	for _, c := range joined(cmd) {
		if strings.Contains(c, "wrong-kubeconfig") {
			t.Errorf("rollback must use the recorded target, got %v", c)
		}
	}
	found := false
	for _, c := range joined(cmd) {
		if strings.Contains(c, recorded) {
			found = true
		}
	}
	if !found {
		t.Error("rollback never touched the recorded target")
	}
}

func TestApply_PersistsKubeconfigTarget(t *testing.T) {
	s, sc, _, target := newEnv(t)

	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// A fresh load from disk (what a later `delete` process does) must see
	// the recorded target.
	reloaded, err := config.LoadConfig(sc.Config.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Clusters) != 1 || reloaded.Clusters[0].KubeconfigTarget != target {
		t.Errorf("KubeconfigTarget not persisted; reloaded = %+v", reloaded.Clusters)
	}
}

func TestApply_NoCAFlagsWhenBundleAbsent(t *testing.T) {
	s, sc, cmd, _ := newEnv(t)
	inner := cmd.RunFunc
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "certificate-authority-data") {
			return nil, nil // LE cluster: CA stripped from admin kubeconfig
		}
		return inner(name, args)
	}

	if err := s.Apply(context.Background(), sc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, c := range joined(cmd) {
		if strings.Contains(c, "set-cluster") && strings.Contains(c, "--certificate-authority") {
			t.Errorf("no CA flags expected when bundle absent: %v", c)
		}
	}
}
