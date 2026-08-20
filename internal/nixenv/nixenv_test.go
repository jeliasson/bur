package nixenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	root := t.TempDir()

	if got := Resolve(root, ""); got != (Ref{}) {
		t.Errorf("empty project should have no devshell, got %+v", got)
	}

	os.WriteFile(filepath.Join(root, "flake.nix"), nil, 0o644)
	if got := Resolve(root, ""); got.Flake != "." || got.File != "" {
		t.Errorf("bare flake should use the default devShell, got %+v", got)
	}

	os.WriteFile(filepath.Join(root, "shell.nix"), nil, 0o644)
	if got := Resolve(root, ""); got.File != filepath.Join(root, "shell.nix") || got.Flake != "" {
		t.Errorf("shell.nix should win over flake.nix, got %+v", got)
	}

	if got := Resolve(root, "./nix/dev.nix"); got.File != filepath.Join(root, "nix/dev.nix") {
		t.Errorf("relative nix.shell should resolve under root, got %+v", got)
	}
	if got := Resolve(root, ".#backend"); got.Flake != ".#backend" || got.File != "" {
		t.Errorf("nix.shell with '#' should be a flake installable, got %+v", got)
	}
}

func TestPruneGCRoots(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache) // os.UserCacheDir honors this on linux
	gcroots := filepath.Join(cache, "bur", "gcroots")
	if err := os.MkdirAll(gcroots, 0o755); err != nil {
		t.Fatal(err)
	}

	alive := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(gcroots, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("aaaa1111.root", alive+"\n")
	write("aaaa1111", "")
	write("bbbb2222.root", filepath.Join(alive, "gone")+"\n")
	write("bbbb2222", "")
	write("bbbb2222-1-link", "")

	pruned, err := PruneGCRoots()
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Errorf("pruned = %d, want 1", pruned)
	}
	for name, want := range map[string]bool{
		"aaaa1111.root": true, "aaaa1111": true,
		"bbbb2222.root": false, "bbbb2222": false, "bbbb2222-1-link": false,
	} {
		_, err := os.Stat(filepath.Join(gcroots, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists = %v, want %v", name, exists, want)
		}
	}
}
