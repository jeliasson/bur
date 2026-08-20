package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Cmd[0] != "claude" {
		t.Errorf("default cmd = %v", cfg.Cmd)
	}
	if cfg.Network != "open" {
		t.Errorf("default network = %q", cfg.Network)
	}
}

func TestMergeSemantics(t *testing.T) {
	global := `
cmd: [claude, --dangerously-skip-permissions]
tools: [openspec, gh]
mounts: ["~/data:/data:ro"]
ports: [3000]
env:
  FOO: global
  BAR: kept
`
	project := `
cmd: [opencode]
tools: [rg]
mounts: ["./fixtures:/fixtures"]
ports: ["8080:80"]
env:
  FOO: project
network: none
hostAccess: true
nix:
  shell: ./nix/dev.nix
`
	cfg := Default()

	gfc, w, err := loadFile(writeTemp(t, "config.yaml", global))
	if err != nil || len(w) != 0 {
		t.Fatalf("global load: %v %v", err, w)
	}
	cfg.apply(gfc)

	pfc, w, err := loadFile(writeTemp(t, ".bur.yaml", project))
	if err != nil || len(w) != 0 {
		t.Fatalf("project load: %v %v", err, w)
	}
	cfg.apply(pfc)

	if cfg.Cmd[0] != "opencode" {
		t.Errorf("cmd not overridden: %v", cfg.Cmd)
	}
	if len(cfg.Tools) != 3 || cfg.Tools[2] != "rg" {
		t.Errorf("tools not concatenated: %v", cfg.Tools)
	}
	if len(cfg.Mounts) != 2 {
		t.Errorf("mounts not concatenated: %v", cfg.Mounts)
	}
	if len(cfg.Ports) != 2 || cfg.Ports[0] != "3000" || cfg.Ports[1] != "8080:80" {
		t.Errorf("ports not concatenated: %v", cfg.Ports)
	}
	if cfg.Env["FOO"] != "project" || cfg.Env["BAR"] != "kept" {
		t.Errorf("env merge wrong: %v", cfg.Env)
	}
	if cfg.Network != "none" || !cfg.HostAccess || cfg.NixShell != "./nix/dev.nix" {
		t.Errorf("scalar overrides wrong: %+v", cfg)
	}
}

func TestUnknownKeyWarning(t *testing.T) {
	_, w, err := loadFile(writeTemp(t, ".bur.yaml", "portss: [8000]\nnix:\n  shells: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 2 {
		t.Fatalf("want 2 warnings, got %v", w)
	}
	if !strings.Contains(w[0], "portss") && !strings.Contains(w[1], "portss") {
		t.Errorf("missing portss warning: %v", w)
	}
	if !strings.Contains(strings.Join(w, " "), "nix.shells") {
		t.Errorf("missing nix.shells warning: %v", w)
	}
}

func TestMissingFileIsNil(t *testing.T) {
	fc, w, err := loadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if fc != nil || w != nil || err != nil {
		t.Errorf("missing file should be nil, got %v %v %v", fc, w, err)
	}
}

func TestValidateNetwork(t *testing.T) {
	cfg := Default()
	cfg.Network = "filtered"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("filtered should be a clear not-yet-supported error, got %v", err)
	}
	cfg.Network = "bogus"
	if err := cfg.validate(); err == nil {
		t.Error("bogus network should error")
	}
}

func TestGuardRoot(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := guardRoot(proj, home); err != nil {
		t.Errorf("project under home should pass: %v", err)
	}
	if err := guardRoot(home, home); err == nil {
		t.Error("root == home must refuse")
	}
	if err := guardRoot(filepath.Dir(home), home); err == nil {
		t.Error("root above home must refuse")
	}
	if err := guardRoot("/", home); err == nil {
		t.Error("root / must refuse")
	}

	// An explicit .bur.yaml at the root is the opt-in.
	if err := os.WriteFile(filepath.Join(home, ".bur.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := guardRoot(home, home); err != nil {
		t.Errorf(".bur.yaml at home should opt in: %v", err)
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindProjectRoot(sub); got != sub {
		t.Errorf("no markers: want cwd %q, got %q", sub, got)
	}

	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	if got := FindProjectRoot(sub); got != root {
		t.Errorf("git root: want %q, got %q", root, got)
	}

	os.WriteFile(filepath.Join(root, "a", ".bur.yaml"), nil, 0o644)
	if got := FindProjectRoot(sub); got != filepath.Join(root, "a") {
		t.Errorf(".bur.yaml wins over .git: got %q", got)
	}
}
