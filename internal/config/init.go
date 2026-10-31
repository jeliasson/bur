package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// envFile: in a config renames DefaultEnvFile.
const (
	ProjectFile    = ".bur.yaml"
	DefaultEnvFile = ".bur.env"
)

var errAborted = errors.New("aborted")

// Every value spelled out is bur's own default, so the scaffold is a no-op
// until edited.
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
#   pkgAdd: true                # let the agent pull extra nixpkgs tools mid-session
#                               # with "bur-pkg add <name>" (host fetches, gone on exit)

# git:                          # identity for sandbox commits. bur generates the
#                               # gitconfig; the host's is never mounted. Set your
#                               # name globally, override it here.
#   name: bur
#   email: bur@noreply.local
#   signingKey: ~/.config/bur/signing_key
#                               # optional commit signing. Use a dedicated
#                               # passphrase-less key (ssh-keygen -t ed25519 -N "")
#                               # uploaded to GitHub as a *signing* key. The agent
#                               # can read it: leaked, it signs as you until revoked
`

const envTemplate = `# bur sandbox secrets - KEY=VALUE per line, no interpolation, no multiline
# values. Loaded on top of the env: block in ` + ProjectFile + `, and gitignored,
# so it can hold what the committed config should not. Renamed via envFile: in
# ` + ProjectFile + ` (default: ` + DefaultEnvFile + `); delete it if this project has no secrets.
#
# The agent sees everything here for the whole session, and with network: open
# it can send it anywhere. Use network: none for sensitive work.

# SOME_API_KEY=
# GITHUB_TOKEN=
`

// Init never overwrites an existing file; force only skips the prompt.
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

// A name that is not a plain file in dir comes back as a skip note: "" opts
// out, and a ~ or absolute path is shared between projects, not init's to create.
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

// Reports whether it wrote.
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
