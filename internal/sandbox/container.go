// Package sandbox owns everything podman: naming and starting one-shot
// containers, exec-ing into them, and listing or cleaning them up.
package sandbox

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jeliasson/bur/internal/config"
	"github.com/jeliasson/bur/internal/ports"
)

// Stamped by main at startup from its -ldflags build vars.
var (
	BaseImageTar = ""
	BaseImageRef = ""
	// Store path the image's /bin symlinks point into, resolved on the host.
	BaseImageRoot = ""
)

const containerHome = "/home/bur"

// entryScript runs as the container main process (and for exec):
// source the devshell env, normalize the basics, run the command.
// The clipboard shims are prepended, not appended: a devshell wl-paste
// or xclip could never reach a compositor from in here, so the bridge
// must win the PATH race for paste to work at all.
const entryScript = `
if [ -f /run/bur/env.sh ]; then . /run/bur/env.sh; fi
export HOME=` + containerHome + ` TMPDIR=/tmp TMP=/tmp TEMP=/tmp TEMPDIR=/tmp
export PATH="$PATH:/bin${BUR_TOOLS_PATH:+:$BUR_TOOLS_PATH}"
if [ -d /run/bur/bin ]; then export PATH="/run/bur/bin:$PATH"; fi
cd "${BUR_WORKDIR:-$HOME}"
exec "$@"
`

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// Sandboxes are one-shot, so names need to be unique, not stable -
// adjective-animal reads better than a pid in `podman ps` and gives each
// sandbox a memorable handle for the exec picker and `bur exec --in`.
var nameAdjectives = []string{
	"bold", "brave", "calm", "cheeky", "clever", "cosy", "daring", "eager",
	"feisty", "gentle", "jolly", "keen", "lively", "merry", "nimble", "perky",
	"plucky", "quirky", "sleepy", "sly", "snug", "spry", "sturdy", "witty",
}

var nameAnimals = []string{
	"badger", "bat", "crow", "ferret", "fox", "gecko", "heron", "lemur",
	"lynx", "marmot", "mole", "moth", "newt", "otter", "owl", "panda",
	"raven", "shrew", "stoat", "tapir", "vole", "wombat", "wren", "yak",
}

func sanitizeBase(root string) string {
	base := unsafeNameChars.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "project"
	}
	return base
}

// ContainerName invents a fresh bur-<project>-<adjective>-<animal> name,
// rerolled while podman knows the name. The pid fallback keeps startup
// going if podman keeps claiming collisions.
func ContainerName(root string) string {
	base := sanitizeBase(root)
	for range 10 {
		name := fmt.Sprintf("bur-%s-%s-%s", base,
			nameAdjectives[rand.IntN(len(nameAdjectives))],
			nameAnimals[rand.IntN(len(nameAnimals))])
		if exec.Command("podman", "container", "exists", name).Run() != nil {
			return name
		}
	}
	return fmt.Sprintf("bur-%s-p%d", base, os.Getpid())
}

func EnsureBaseImage() error {
	if BaseImageRef == "" {
		if os.Getenv("BUR_BASE_IMAGE") != "" {
			BaseImageRef = os.Getenv("BUR_BASE_IMAGE")
			return nil
		}
		return fmt.Errorf("no base image embedded in this build (set BUR_BASE_IMAGE for dev builds)")
	}
	// Gone from the host store means the image's /bin dangles - no shell.
	if BaseImageRoot != "" {
		if _, err := os.Stat(BaseImageRoot); err != nil {
			fmt.Fprintf(os.Stderr, "bur: warning: %s is missing from the host store, the sandbox may have no working shell (nix-store --realise it, or reinstall bur)\n", BaseImageRoot)
		}
	}
	if exec.Command("podman", "image", "exists", BaseImageRef).Run() == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "bur: loading base image %s\n", BaseImageRef)
	cmd := exec.Command("podman", "load", "-i", BaseImageTar)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolveArgv0 resolves the command through the host PATH to a nix store
// path so it works inside the container regardless of the devshell PATH.
// Only the directory is resolved - the leaf symlink must survive so
// multi-call binaries (coreutils) still see their applet name in argv[0].
func resolveArgv0(argv []string) []string {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return argv
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || !strings.HasPrefix(dir, "/nix/store/") {
		return argv
	}
	out := append([]string{}, argv...)
	out[0] = filepath.Join(dir, filepath.Base(path))
	return out
}

// resolveToolDirs maps configured tool names to their nix store bin
// directories, for appending to the container PATH. The full binary path
// is resolved (not just its directory, as resolveArgv0 does): host PATH
// entries are typically profile symlink farms spanning every installed
// package, and appending one of those would expose the whole host profile
// instead of the listed tools.
func resolveToolDirs(tools []string) []string {
	var dirs []string
	seen := map[string]bool{}
	for _, name := range tools {
		path, err := exec.LookPath(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bur: warning: tool %q not found on host PATH, skipping\n", name)
			continue
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || !strings.HasPrefix(real, "/nix/store/") {
			fmt.Fprintf(os.Stderr, "bur: warning: tool %q does not resolve to /nix/store, skipping\n", name)
			continue
		}
		dir := filepath.Dir(real)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// AgentHint names the agent being sandboxed for herdr's benefit. Herdr
// identifies agent panes by scanning the pane's foreground processes, but
// the real agent process is hidden behind conmon inside the container, so
// bur advertises it via HERDR_AGENT on the host-visible podman process.
// Herdr ignores names it doesn't recognize, so no filtering is needed.
func AgentHint(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(argv[0]))
}

// hostPath expands ~ and resolves symlinks (podman needs real paths;
// e.g. ~/.claude is itself a symlink into the nix repo).
func hostPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func parseMount(spec string) (string, error) {
	parts := strings.Split(spec, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("invalid mount %q (want host:container[:ro])", spec)
	}
	src, err := hostPath(parts[0])
	if err != nil {
		return "", fmt.Errorf("mount %q: %w", spec, err)
	}
	out := src + ":" + parts[1]
	if len(parts) == 3 {
		out += ":" + parts[2]
	}
	return out, nil
}

type RunSpec struct {
	Name     string
	Root     string
	Workdir  string
	EnvDir   string
	Cfg      config.Config
	Ports    []ports.Mapping
	HasShell bool
}

func BuildRunArgs(s RunSpec, argv []string) ([]string, error) {
	args := []string{"run", "--rm", "-i",
		"--name", s.Name,
		"--hostname", "bur",
		"--label", "bur.agent=" + AgentHint(argv),
		"--label", "bur.root=" + s.Root,
		"--label", "bur.cwd=" + s.Workdir,
		"--userns=keep-id",
		"-v", s.Root + ":" + s.Root,
		"-v", "/nix/store:/nix/store:ro",
		"--tmpfs", containerHome + ":rw,exec",
		"-e", "HOME=" + containerHome,
		"-e", "BUR_WORKDIR=" + s.Workdir,
	}
	if term, ok := os.LookupEnv("TERM"); ok {
		args = append(args, "-e", "TERM="+term)
	}
	if isTerminal() {
		args = append(args, "-t")
	}
	if s.EnvDir != "" {
		args = append(args, "-v", s.EnvDir+":/run/bur:ro")
	}
	if dirs := resolveToolDirs(s.Cfg.Tools); len(dirs) > 0 {
		args = append(args, "-e", "BUR_TOOLS_PATH="+strings.Join(dirs, ":"))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	for _, state := range []string{".claude", ".claude.json"} {
		src, err := hostPath(filepath.Join(home, state))
		if err != nil {
			if AgentHint(argv) == "claude" {
				fmt.Fprintf(os.Stderr, "bur: note: %s not found on host, claude state will not persist\n", "~/"+state)
			}
			continue
		}
		args = append(args, "-v", src+":"+containerHome+"/"+state)
	}

	for _, m := range s.Cfg.Mounts {
		v, err := parseMount(m)
		if err != nil {
			return nil, err
		}
		args = append(args, "-v", v)
	}

	// Config env travels in a file, never as -e flags: the podman process
	// stays in the foreground for the sandbox's whole lifetime, and its
	// command line is world-readable in /proc - no place for secrets.
	if len(s.Cfg.Env) > 0 {
		envFile, err := writeEnvFile(s.EnvDir, s.Cfg.Env)
		if err != nil {
			return nil, err
		}
		args = append(args, "--env-file", envFile)
	}

	if s.Cfg.Network == "none" {
		args = append(args, "--network=none")
	} else {
		for _, p := range s.Ports {
			args = append(args, "-p", fmt.Sprintf("%s:%d:%d", p.BindIP(), p.Host, p.Container))
		}
		if s.Cfg.HostAccess {
			args = append(args, "--add-host", "host.containers.internal:host-gateway")
		}
	}

	args = append(args, BaseImageRef, "/bin/bash", "-c", entryScript, "bur")
	args = append(args, resolveArgv0(argv)...)
	return args, nil
}

// writeEnvFile renders config env as a KEY=VALUE file in the env dir
// (0700, one per sandbox) for podman's --env-file. The format is
// line-based with no escaping, so newlines cannot be represented.
func writeEnvFile(envDir string, env map[string]string) (string, error) {
	if envDir == "" {
		return "", fmt.Errorf("env configured but no env dir to hold it")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := env[k]
		if strings.ContainsRune(k+v, '\n') {
			return "", fmt.Errorf("env %s: newlines in names or values are not supported", k)
		}
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	path := filepath.Join(envDir, "config.env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func BuildExecArgs(name string, argv []string) []string {
	args := []string{"exec", "-i"}
	if isTerminal() {
		args = append(args, "-t")
	}
	args = append(args, name, "/bin/bash", "-c", entryScript, "bur")
	args = append(args, resolveArgv0(argv)...)
	return args
}

func RunPodman(args []string, agent string) error {
	cmd := exec.Command("podman", args...)
	// Herdr reads /proc/<pid>/environ, which is fixed at exec time - the
	// hint must be set on the podman child, os.Setenv here would be invisible.
	if agent != "" && os.Getenv("HERDR_AGENT") == "" {
		cmd.Env = append(os.Environ(), "HERDR_AGENT="+agent)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
