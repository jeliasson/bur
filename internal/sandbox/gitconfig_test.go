package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeliasson/bur/internal/config"
)

func TestWriteGitConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{GitName: `Johan "J" O'Brien\`, GitEmail: "johan@example.com"}
	if err := writeGitConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		`name = "Johan \"J\" O'Brien\\"`,
		`email = "johan@example.com"`,
		"gpgsign = false",
		"defaultBranch = main",
		"helper = ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("gitconfig missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "signingkey") || strings.Contains(got, "format = ssh") {
		t.Errorf("no signing config expected without a key:\n%s", got)
	}
}

func TestWriteGitConfigSigning(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{GitName: "bur", GitEmail: "bur@noreply.local", GitSigningKey: "~/.config/bur/signing_key"}
	if err := writeGitConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		"signingkey = " + signingKeyPath,
		"format = ssh",
		"gpgsign = true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("gitconfig missing %q:\n%s", want, got)
		}
	}
}

func TestBuildRunArgsGit(t *testing.T) {
	key := filepath.Join(t.TempDir(), "signing_key")
	if err := os.WriteFile(key, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Name:    "bur-test-witty-yak",
		Root:    t.TempDir(),
		Workdir: "/tmp",
		EnvDir:  t.TempDir(),
		Cfg: config.Config{
			Cmd:           []string{"bash"},
			Network:       "open",
			GitName:       "bur",
			GitEmail:      "bur@noreply.local",
			GitSigningKey: key,
		},
	}
	args, err := BuildRunArgs(spec, []string{"bash"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(spec.EnvDir, "gitconfig")); err != nil {
		t.Errorf("gitconfig not written to env dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spec.EnvDir, "signing_key")); err != nil {
		t.Errorf("signing key mount point not created: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, key+":"+signingKeyPath+":ro") {
		t.Errorf("signing key not mounted read-only: %v", args)
	}

	spec.Cfg.GitSigningKey = filepath.Join(t.TempDir(), "missing")
	if _, err := BuildRunArgs(spec, []string{"bash"}); err == nil || !strings.Contains(err.Error(), "signingKey") {
		t.Errorf("missing signing key should error with a hint, got %v", err)
	}
}
