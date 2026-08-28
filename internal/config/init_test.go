package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func yes(string) bool { return true }
func no(string) bool  { return false }

// noPrompt fails the test on any prompt - for runs that must not ask.
func noPrompt(t *testing.T) func(string) bool {
	return func(p string) bool {
		t.Fatalf("unexpected prompt: %s", p)
		return false
	}
}

// The templates must parse as the thing they claim to be.
func TestProjectTemplateParses(t *testing.T) {
	fc, warnings, err := loadFile(writeTemp(t, ProjectFile, projectTemplate))
	if err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("template has unknown keys: %v", warnings)
	}
	cfg := Default()
	cfg.apply(fc)
	def := Default()
	if cfg.Cmd[0] != def.Cmd[0] || cfg.Network != def.Network ||
		cfg.HostAccess != def.HostAccess || cfg.Clipboard != def.Clipboard ||
		cfg.EnvFile != def.EnvFile {
		t.Errorf("template is not a no-op: %+v", cfg)
	}
}

func TestEnvTemplateIsAllComments(t *testing.T) {
	env, warnings, err := loadEnvFile(writeTemp(t, DefaultEnvFile, envTemplate))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("template: %v %v", err, warnings)
	}
	if len(env) != 0 {
		t.Errorf("template sets env: %v", env)
	}
}

func TestInitScaffoldsEverythingOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A repo root asks nothing and writes all three files.
	if err := initFiles(dir, noPrompt(t)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ProjectFile, DefaultEnvFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err != nil ||
		!strings.Contains(string(data), DefaultEnvFile) {
		t.Errorf(".gitignore = %q, %v", data, err)
	}

	// A second run leaves existing files alone.
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte("# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultEnvFile), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initFiles(dir, noPrompt(t)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ProjectFile)); string(data) != "# edited\n" {
		t.Errorf("%s = %q, want untouched", ProjectFile, data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, DefaultEnvFile)); string(data) != "SECRET=1\n" {
		t.Errorf("%s = %q, want untouched", DefaultEnvFile, data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); strings.Count(string(data), DefaultEnvFile) != 1 {
		t.Errorf(".gitignore = %q, want a single entry", data)
	}
}

func TestInitOutsideRepoRootAsks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir() // no .git

	if err := initFiles(dir, no); err != errAborted {
		t.Fatalf("declined location: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectFile)); err == nil {
		t.Fatal("file written after abort")
	}

	if err := initFiles(dir, yes); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ProjectFile)); err != nil {
		t.Fatalf("file not written after yes: %v", err)
	}
}

// The fresh-clone case: a committed .bur.yaml renames the env file, and
// init creates just that.
func TestInitScaffoldsRenamedEnvFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte("envFile: .env\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := initFiles(dir, noPrompt(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		t.Errorf(".env not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DefaultEnvFile)); err == nil {
		t.Errorf("%s written despite the rename", DefaultEnvFile)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); !strings.Contains(string(data), ".env") {
		t.Errorf(".gitignore = %q, want .env", data)
	}
}

// A non-local envFile: skips the secrets half without failing the rest.
func TestInitSkipsNonLocalEnvFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, spec := range []string{`envFile: ""`, `envFile: "~/secrets.env"`, "envFile: ../secrets.env"} {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte(spec+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := initFiles(dir, noPrompt(t)); err != nil {
			t.Errorf("%s: %v", spec, err)
		}
		if _, err := os.Stat(filepath.Join(dir, DefaultEnvFile)); err == nil {
			t.Errorf("%s: %s written", spec, DefaultEnvFile)
		}
		if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
			t.Errorf("%s: .gitignore written", spec)
		}
	}
}

func TestEnsureGitignore(t *testing.T) {
	// Missing .gitignore, existing one without a trailing newline, and
	// one that already covers the entry in either spelling.
	cases := []struct {
		name   string
		start  string
		exists bool
		added  bool
		want   string
	}{
		{name: "created", added: true, want: ".bur.env\n"},
		{name: "appended", start: "result\n", exists: true, added: true, want: "result\n.bur.env\n"},
		{name: "no trailing newline", start: "result", exists: true, added: true, want: "result\n.bur.env\n"},
		{name: "already there", start: "result\n.bur.env\n", exists: true, want: "result\n.bur.env\n"},
		{name: "already there rooted", start: "/.bur.env\n", exists: true, want: "/.bur.env\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			if tc.exists {
				if err := os.WriteFile(path, []byte(tc.start), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			added, err := ensureGitignore(dir, DefaultEnvFile)
			if err != nil {
				t.Fatal(err)
			}
			if added != tc.added {
				t.Errorf("added = %v, want %v", added, tc.added)
			}
			if data, _ := os.ReadFile(path); string(data) != tc.want {
				t.Errorf("gitignore = %q, want %q", data, tc.want)
			}
		})
	}
}

func TestLoadEnvFile(t *testing.T) {
	content := `# a comment

FOO=bar
  SPACED = padded
QUOTED="two words"
SINGLE='literal'
export EXPORTED=yes
EMPTY=
URL=https://example.com/a=b
nonsense
`
	env, warnings, err := loadEnvFile(writeTemp(t, DefaultEnvFile, content))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], ":10:") {
		t.Errorf("warnings = %v, want one for line 10", warnings)
	}
	want := map[string]string{
		"FOO": "bar", "SPACED": "padded", "QUOTED": "two words",
		"SINGLE": "literal", "EXPORTED": "yes", "EMPTY": "",
		"URL": "https://example.com/a=b",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env = %v, want %d keys", env, len(want))
	}
}

func TestEnvFileOverridesYaml(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // ignore the real global config
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ProjectFile),
		[]byte("env:\n  TOKEN: from-yaml\n  KEPT: yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DefaultEnvFile),
		[]byte("TOKEN=from-env-file\nEXTRA=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, gotRoot, warnings, err := Load(root)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if cfg.Env["TOKEN"] != "from-env-file" {
		t.Errorf("TOKEN = %q, want the .bur.env value", cfg.Env["TOKEN"])
	}
	if cfg.Env["KEPT"] != "yaml" || cfg.Env["EXTRA"] != "1" {
		t.Errorf("env = %v, want both files merged", cfg.Env)
	}
}

func TestEnvFileRenamed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(ProjectFile, "envFile: .env\n")
	write(".env", "TOKEN=from-dot-env\n")
	write(DefaultEnvFile, "TOKEN=ignored\n")

	cfg, _, warnings, err := Load(root)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	if cfg.Env["TOKEN"] != "from-dot-env" {
		t.Errorf("TOKEN = %q, want the renamed file's value", cfg.Env["TOKEN"])
	}

	// A missing configured name warns; a missing default is silent.
	write(ProjectFile, "envFile: .missing\n")
	if _, _, warnings, err = Load(root); err != nil || len(warnings) != 1 ||
		!strings.Contains(warnings[0], ".missing") {
		t.Errorf("missing envFile: warnings = %v, err = %v", warnings, err)
	}

	// And an empty envFile opts out of the mechanism entirely.
	write(ProjectFile, "envFile: \"\"\n")
	cfg, _, warnings, err = Load(root)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	if len(cfg.Env) != 0 {
		t.Errorf("env = %v, want nothing read", cfg.Env)
	}
}

func TestEnvFileName(t *testing.T) {
	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	dir := t.TempDir()
	if name, skip, err := envFileName(dir); name != DefaultEnvFile || skip != "" || err != nil {
		t.Errorf("no config: %q %q %v", name, skip, err)
	}

	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cmd: [bash]\n")
	if name, skip, err := envFileName(dir); name != DefaultEnvFile || skip != "" || err != nil {
		t.Errorf("config without envFile: %q %q %v", name, skip, err)
	}
	write("envFile: .env\n")
	if name, skip, err := envFileName(dir); name != ".env" || skip != "" || err != nil {
		t.Errorf("renamed: %q %q %v", name, skip, err)
	}
	// Non-local names skip rather than error.
	for _, spec := range []string{"envFile: \"~/secrets.env\"\n", "envFile: ../secrets.env\n", "envFile: \"\"\n"} {
		write(spec)
		if name, skip, err := envFileName(dir); name != "" || skip == "" || err != nil {
			t.Errorf("%q: %q %q %v, want a skip note", spec, name, skip, err)
		}
	}

	// The global config renames too; the project file still wins.
	if err := os.MkdirAll(filepath.Join(conf, "bur"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "bur", "config.yaml"),
		[]byte("envFile: .env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ProjectFile)); err != nil {
		t.Fatal(err)
	}
	if name, _, err := envFileName(dir); name != ".env" || err != nil {
		t.Errorf("global rename: %q %v", name, err)
	}
	write("envFile: .secrets\n")
	if name, _, err := envFileName(dir); name != ".secrets" || err != nil {
		t.Errorf("project over global: %q %v", name, err)
	}
}
