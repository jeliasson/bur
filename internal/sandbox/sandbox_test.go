package sandbox

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeliasson/bur/internal/config"
)

func TestSanitizeBase(t *testing.T) {
	for in, want := range map[string]string{
		"/home/johan/nix":       "nix",
		"/srv/Weird Dir!!":      "weird-dir",
		"/":                     "project",
		"/home/johan/My.Repo_2": "my-repo-2",
	} {
		if got := sanitizeBase(in); got != want {
			t.Errorf("sanitizeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrderCandidates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	cands := []Sandbox{
		{Name: "old-elsewhere", Cwd: "/repo", Started: t0},
		{Name: "new-elsewhere", Cwd: "/repo", Started: t0.Add(time.Hour)},
		{Name: "old-here", Cwd: "/repo/sub", Started: t0.Add(-time.Hour)},
	}
	orderCandidates(cands, "/repo/sub")
	want := []string{"old-here", "new-elsewhere", "old-elsewhere"}
	for i, w := range want {
		if cands[i].Name != w {
			t.Fatalf("order[%d] = %s, want %s (full: %v)", i, cands[i].Name, w, cands)
		}
	}
}

func TestMatchByName(t *testing.T) {
	all := []Sandbox{
		{Name: "bur-nix-brave-otter"},
		{Name: "bur-nix-sly-otter"},
		{Name: "bur-tmp-witty-yak"},
	}

	if got, err := MatchByName(all, "bur-nix-brave-otter"); err != nil || got != "bur-nix-brave-otter" {
		t.Errorf("exact: %q %v", got, err)
	}
	if got, err := MatchByName(all, "witty-yak"); err != nil || got != "bur-tmp-witty-yak" {
		t.Errorf("suffix: %q %v", got, err)
	}
	if got, err := MatchByName(all, "yak"); err != nil || got != "bur-tmp-witty-yak" {
		t.Errorf("animal only: %q %v", got, err)
	}
	if _, err := MatchByName(all, "otter"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous suffix should error, got %v", err)
	}
	if _, err := MatchByName(all, "gone"); err == nil || !strings.Contains(err.Error(), "bur ls") {
		t.Errorf("missing name should point at bur ls, got %v", err)
	}
}

func TestBuildRunArgsEnvFile(t *testing.T) {
	spec := RunSpec{
		Name:    "bur-test-witty-yak",
		Root:    t.TempDir(),
		Workdir: "/tmp",
		EnvDir:  t.TempDir(),
		Cfg: config.Config{
			Cmd:     []string{"bash"},
			Env:     map[string]string{"ZED": "2", "ABC": "1"},
			Network: "open",
		},
	}
	args, err := BuildRunArgs(spec, []string{"bash"})
	if err != nil {
		t.Fatal(err)
	}

	var envFile string
	for i, a := range args {
		if a == "-e" && strings.Contains(args[i+1], "ABC") {
			t.Errorf("config env leaked onto the podman command line: %v", args)
		}
		if a == "--env-file" {
			envFile = args[i+1]
		}
	}
	if envFile == "" {
		t.Fatalf("no --env-file in %v", args)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ABC=1\nZED=2\n" {
		t.Errorf("env file content %q, want sorted KEY=VALUE lines", data)
	}

	spec.Cfg.Env = map[string]string{"BAD": "a\nb"}
	if _, err := BuildRunArgs(spec, []string{"bash"}); err == nil {
		t.Error("newline in env value must be rejected")
	}
}

func TestContainerNameShape(t *testing.T) {
	// No podman in the test environment: `podman container exists` fails,
	// which ContainerName treats as "name is free" - fine for shape checks.
	name := ContainerName("/home/johan/nix")
	parts := strings.Split(name, "-")
	if len(parts) != 4 || parts[0] != "bur" || parts[1] != "nix" {
		t.Fatalf("unexpected name shape: %q", name)
	}
}
