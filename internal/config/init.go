package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The two files `bur init` scaffolds; `envFile:` renames the second.
const (
	ProjectFile    = ".bur.yaml"
	DefaultEnvFile = ".bur.env"
)

var errAborted = errors.New("aborted")

// projectTemplate spells out the defaults bur already applies, so the
// file is a no-op until edited.
const projectTemplate = `# bur project config - https://github.com/jeliasson/bur
#
# Merged over the global ~/.config/bur/config.yaml: scalars override,
# lists concatenate, env merges. Every value below is bur's default,
# so this file changes nothing until you edit it.

cmd: [claude, --dangerously-skip-permissions]   # what the sandbox runs
network: open                   # open | none  ("filtered" reserved for v2)
hostAccess: false               # no host.containers.internal
clipboard: true                 # read-only paste bridge (text & images)

# tools:                        # agent companion CLIs, resolved from the host PATH
#   - openspec
#   - gh
#   - rg

# ports:                        # published on 127.0.0.1; a taken host port falls
#                               # through to the next free one. Prefix a host IP to
#                               # widen: "0.0.0.0:5173:5173" exposes to the LAN
#   - 8000
#   - "5173:5173"

# mounts:                       # host:container[:ro]
#   - "~/fixtures:/fixtures:ro"

# env:                          # keep secrets out of here - see ` + DefaultEnvFile + `
#   TARS_HONESTY_LEVEL: "0.9"

# envFile: .env                 # where the secrets above live instead, relative
#                               # to this file (default: ` + DefaultEnvFile + `; "" reads none)

# nix:
#   shell: ./shell.nix          # a nix file, or a flake installable like ".#dev"
#                               # default: shell.nix, else the flake's default devShell
`

const envTemplate = `# bur sandbox secrets - KEY=VALUE per line, no interpolation, no multiline
# values. Loaded on top of the env: block in ` + ProjectFile + `, and kept out of
# git so it can hold what the committed config should not. bur finds it as
# envFile: in ` + ProjectFile + ` (default: ` + DefaultEnvFile + `); a project without
# secrets can simply delete it.
#
# Everything here is visible to the agent for the whole session, and with
# network: open an agent can send it anywhere. Set network: none in
# ` + ProjectFile + ` for sensitive work.

# ANTHROPIC_API_KEY=
# GITHUB_TOKEN=
`

// Init scaffolds the missing project files in dir: .bur.yaml and the
// gitignored secrets file it names via envFile:. Nothing is ever
// overwritten; force only skips the repo-root prompt.
func Init(dir string, force bool) error {
	return initFiles(dir, asker(force))
}

func initFiles(dir string, ask func(string) bool) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if !ask(fmt.Sprintf("%s is not the root of a git repository - create project files here anyway?", dir)) {
			return errAborted
		}
	}

	if err := scaffold(dir, ProjectFile, projectTemplate,
		"holds bur's defaults, so nothing changes until you edit it"); err != nil {
		return err
	}

	name, skip, err := envFileName(dir)
	if err != nil {
		return err
	}
	if skip != "" {
		fmt.Println("-", skip)
		return nil
	}
	if err := scaffold(dir, name, envTemplate,
		"for secrets; delete it if this project has none"); err != nil {
		return err
	}

	switch added, err := ensureGitignore(dir, name); {
	case err != nil:
		return fmt.Errorf(".gitignore not updated: %w", err)
	case added:
		fmt.Println("- Added", name, "to", filepath.Join(dir, ".gitignore"))
	default:
		fmt.Println("- File", name, "is already gitignored")
	}
	return nil
}

// envFileName resolves the secrets file name through both configs, so
// init cannot scaffold a file nothing reads. A name that is not a plain
// file in dir comes back as a skip note: "" opts out, and a ~ or
// absolute path is shared between projects, not init's to create.
func envFileName(dir string) (string, string, error) {
	name := DefaultEnvFile
	paths := []string{filepath.Join(dir, ProjectFile)}
	if confDir, err := os.UserConfigDir(); err == nil {
		paths = append([]string{filepath.Join(confDir, "bur", "config.yaml")}, paths...)
	}
	for _, path := range paths {
		fc, _, err := loadFile(path)
		if err != nil {
			return "", "", err
		}
		if fc != nil && fc.EnvFile != nil {
			name = *fc.EnvFile
		}
	}
	switch {
	case name == "":
		return "", `envFile is "" - no secrets file to scaffold`, nil
	case strings.ContainsRune(name, filepath.Separator) || strings.HasPrefix(name, "~"):
		return "", fmt.Sprintf("envFile: %s is not a file in this directory - create and gitignore it yourself", name), nil
	}
	return name, "", nil
}

// scaffold writes content to dir/name unless the file already exists.
func scaffold(dir, name, content, what string) error {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		fmt.Println("- File", path, "exists, left alone")
		return nil
	}
	// 0600: the secrets file needs it; on .bur.yaml it costs nothing.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Printf("- Wrote %s - %s\n", path, what)
	return nil
}

// ensureGitignore appends entry to dir/.gitignore unless a line already
// covers it. Reports whether it wrote.
func ensureGitignore(dir, entry string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case entry, "/" + entry:
			return false, nil
		}
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return true, os.WriteFile(path, []byte(out+entry+"\n"), 0o644)
}

// asker turns -f into a prompter that always says yes.
func asker(force bool) func(string) bool {
	if force {
		return func(string) bool { return true }
	}
	return confirm
}

// Shared across prompts: a fresh bufio.Reader per question would drop
// input buffered past its newline.
var stdin = bufio.NewReader(os.Stdin)

func confirm(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	line, err := stdin.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
