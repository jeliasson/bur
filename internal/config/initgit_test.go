package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGitFresh(t *testing.T) {
	dir := t.TempDir()
	keygenPath := ""
	err := initGit(dir,
		func(string) bool { return true },
		func() (string, string) { return "Johan", "johan@example.com" },
		func(path string) error { keygenPath = path; return os.WriteFile(path, []byte("key"), 0o600) })
	if err != nil {
		t.Fatal(err)
	}
	if keygenPath != filepath.Join(dir, "signing_key") {
		t.Errorf("keygen path = %q", keygenPath)
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`name: "Johan"`, `email: "johan@example.com"`, "signingKey: ~/.config/bur/signing_key"} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}

	// The result must parse back cleanly into the same identity.
	fc, w, err := loadFile(filepath.Join(dir, "config.yaml"))
	if err != nil || len(w) != 0 {
		t.Fatalf("reload: %v %v", err, w)
	}
	cfg := Default()
	cfg.apply(fc)
	if cfg.GitName != "Johan" || cfg.GitEmail != "johan@example.com" {
		t.Errorf("roundtrip identity wrong: %+v", cfg)
	}
}

func TestInitGitCreatesMissingDirPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bur")
	err := initGit(dir,
		func(string) bool { return true },
		func() (string, string) { return "Johan", "johan@example.com" },
		func(path string) error { return os.WriteFile(path, []byte("key"), 0o600) })
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
}

func TestInitGitTightensDirWithKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := initGit(dir,
		func(string) bool { return true },
		func() (string, string) { return "", "" },
		func(path string) error { return os.WriteFile(path, []byte("key"), 0o600) })
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
}

func TestInitGitDeclinedKeepsNeutralDefault(t *testing.T) {
	dir := t.TempDir()
	err := initGit(dir,
		func(string) bool { return false },
		func() (string, string) { return "Johan", "johan@example.com" },
		func(string) error { t.Error("keygen must not run when declined"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), `name: "bur"`) || strings.Contains(string(data), "signingKey") {
		t.Errorf("declined prompts should yield neutral identity, no key:\n%s", data)
	}
}

func TestInitGitAppendsAndLeavesExistingBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("network: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yes := func(string) bool { return true }
	ident := func() (string, string) { return "Johan", "johan@example.com" }
	nokey := func(string) error { return nil }

	if err := initGit(dir, yes, ident, nokey); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "network: none") || !strings.Contains(string(data), "git:") {
		t.Errorf("existing content not preserved or block missing:\n%s", data)
	}

	// A second run must not duplicate the block.
	if err := initGit(dir, yes, ident, nokey); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), "git:") != 1 {
		t.Errorf("git block duplicated:\n%s", data)
	}
}

func TestInitGitExistingKeyEnablesSigning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "signing_key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := initGit(dir,
		func(string) bool { return true },
		func() (string, string) { return "", "" },
		func(string) error { t.Error("keygen must not run for an existing key"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(data), "signingKey") {
		t.Errorf("existing key should be referenced:\n%s", data)
	}
}
