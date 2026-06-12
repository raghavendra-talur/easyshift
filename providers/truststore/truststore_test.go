package truststore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheEasyShift/easyshift/providers/fakes"
)

func calls(cmd *fakes.CommandRunner) []string {
	var out []string
	for _, c := range cmd.Calls {
		out = append(out, c.Name+" "+strings.Join(c.Args, " "))
	}
	return out
}

func newTestInstaller(t *testing.T, goos string, existing ...string) (*Installer, *fakes.CommandRunner, string) {
	t.Helper()
	home := t.TempDir()
	cmd := &fakes.CommandRunner{}
	exists := map[string]bool{}
	for _, p := range existing {
		exists[p] = true
	}
	ins := New(cmd)
	ins.goos = goos
	ins.home = home
	ins.pathExists = func(p string) bool {
		if strings.HasPrefix(p, home) {
			_, err := os.Stat(p)
			return err == nil
		}
		return exists[p]
	}
	ins.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	return ins, cmd, home
}

func TestInstall_FedoraFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := calls(cmd)
	want := []string{
		"sudo cp /cfg/ca/ca.crt /etc/pki/ca-trust/source/anchors/easyshift-local-ca.crt",
		"sudo update-ca-trust extract",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("call %d = %v, want %q", i, got, w)
		}
	}
}

func TestInstall_DebianFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/usr/local/share/ca-certificates")

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "/usr/local/share/ca-certificates/easyshift-local-ca.crt") ||
		!strings.Contains(got, "sudo update-ca-certificates") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestInstall_NoKnownStore(t *testing.T) {
	ins, _, _ := newTestInstaller(t, "linux")
	err := ins.Install(context.Background(), "/cfg/ca/ca.crt")
	if err == nil || !strings.Contains(err.Error(), "/etc/pki/ca-trust/source/anchors") {
		t.Errorf("want error naming the expected locations, got %v", err)
	}
}

func TestInstall_Darwin(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "darwin")
	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /cfg/ca/ca.crt") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestInstall_NSSDatabases(t *testing.T) {
	ins, cmd, home := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	ins.lookPath = func(string) (string, error) { return "/usr/bin/certutil", nil }

	// One user NSS db + one Firefox profile with cert9.db.
	nss := filepath.Join(home, ".pki", "nssdb")
	profile := filepath.Join(home, ".mozilla", "firefox", "abc.default")
	for _, d := range []string{nss, profile} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profile, "cert9.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	for _, want := range []string{
		"certutil -A -d sql:" + nss,
		"certutil -A -d sql:" + profile,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUninstall_FedoraFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	if err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "sudo rm -f /etc/pki/ca-trust/source/anchors/easyshift-local-ca.crt") ||
		!strings.Contains(got, "sudo update-ca-trust extract") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestUninstall_DebianFamily(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/usr/local/share/ca-certificates")
	if err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	if !strings.Contains(got, "sudo rm -f /usr/local/share/ca-certificates/easyshift-local-ca.crt") ||
		!strings.Contains(got, "sudo update-ca-certificates") {
		t.Errorf("unexpected calls:\n%s", got)
	}
}

func TestUninstall_Darwin(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "darwin")
	if err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	all := calls(cmd)
	if len(all) < 2 {
		t.Fatalf("want at least 2 calls, got %v", all)
	}
	wantFirst := "sudo security remove-trusted-cert -d /cfg/ca/ca.crt"
	if all[0] != wantFirst {
		t.Errorf("first call = %q, want %q", all[0], wantFirst)
	}
	wantSecond := `sudo security delete-certificate -c easyshift local CA /Library/Keychains/System.keychain`
	found := false
	for _, c := range all {
		if c == wantSecond {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing delete-certificate call %q in %v", wantSecond, all)
	}
}

func TestUninstall_Darwin_ToleratesNotFound(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "darwin")
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, "remove-trusted-cert") {
			return []byte("SecTrustSettingsRemoveTrustSettings: The specified item could not be found in the keychain."),
				errors.New("exit status 1")
		}
		if strings.Contains(joined, "delete-certificate") {
			return []byte(`Unable to delete certificate matching "easyshift local CA"`),
				errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Errorf("Uninstall should return nil on not-found, got %v", err)
	}
}

func TestUninstall_Darwin_PropagatesSudoFailure(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "darwin")
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, "remove-trusted-cert") {
			return []byte("sudo: 3 incorrect password attempts"), errors.New("exit status 1")
		}
		return nil, nil
	}
	err := ins.Uninstall(context.Background(), "/cfg/ca/ca.crt")
	if err == nil {
		t.Fatal("Uninstall should return error on sudo failure, got nil")
	}
	if !strings.Contains(err.Error(), "remove trust settings") {
		t.Errorf("error should mention 'remove trust settings', got %v", err)
	}
}

func TestInstall_SnapAndFlatpakFirefoxProfiles(t *testing.T) {
	ins, cmd, home := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	ins.lookPath = func(string) (string, error) { return "/usr/bin/certutil", nil }

	snapProfile := filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox", "x.default")
	flatpakProfile := filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox", "y.default")
	for _, d := range []string{snapProfile, flatpakProfile} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cert9.db"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := strings.Join(calls(cmd), "\n")
	for _, want := range []string{
		"certutil -A -d sql:" + snapProfile,
		"certutil -A -d sql:" + flatpakProfile,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestInstall_NoCertutilMakesNoNSSCalls(t *testing.T) {
	ins, cmd, _ := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	// lookPath already set to fail by newTestInstaller default.

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, c := range cmd.Calls {
		if c.Name == "certutil" {
			t.Errorf("unexpected certutil call: %v", c)
		}
	}
}

func TestInstall_NSSFailureIsBestEffort(t *testing.T) {
	ins, cmd, home := newTestInstaller(t, "linux", "/etc/pki/ca-trust/source/anchors")
	ins.lookPath = func(string) (string, error) { return "/usr/bin/certutil", nil }
	cmd.RunFunc = func(name string, args []string) ([]byte, error) {
		if name == "certutil" {
			return nil, errors.New("certutil: database locked")
		}
		return nil, nil
	}

	// Create a Firefox profile so certutil gets called.
	profile := filepath.Join(home, ".mozilla", "firefox", "abc.default")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "cert9.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ins.Install(context.Background(), "/cfg/ca/ca.crt"); err != nil {
		t.Errorf("Install should return nil even when certutil fails, got %v", err)
	}
}
