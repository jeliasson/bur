package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jeliasson/bur/internal/clipboard"
	"github.com/jeliasson/bur/internal/config"
	"github.com/jeliasson/bur/internal/nixenv"
	"github.com/jeliasson/bur/internal/pkgbridge"
	"github.com/jeliasson/bur/internal/ports"
	"github.com/jeliasson/bur/internal/sandbox"
)

// Set via -ldflags at build time (-X main.version etc.); wired into the
// internal packages at startup.
var (
	version       = "dev"
	baseImageTar  = ""
	baseImageRef  = ""
	baseImageRoot = ""
)

const usage = `bur - run an AI agent (or anything) in a one-shot rootless podman sandbox

Usage:
  bur              start a sandbox with the configured command (default: claude)
  bur <cmd> ...    start a sandbox with <cmd> instead
  bur exec [--in <name>] <cmd> ...
                   run a command in a running sandbox of this project
  bur ls           list running sandboxes
  bur init [-f]    write a starter .bur.yaml and a gitignored .bur.env for
                   secrets - only what is missing (-f skips the prompt)
  bur clean [-f]   remove all bur containers and stale devshell gc roots (asks first)
  bur --version    print the bur version

A sandbox lives exactly as long as its command: exit the agent and the
container is gone. Run as many per project as you like.

Config: ~/.config/bur/config.yaml (global), .bur.yaml (project),
        .bur.env (project secrets, uncommitted; rename it with envFile:).`

func main() {
	clipboard.Version = version
	pkgbridge.Version = version
	sandbox.BaseImageTar = baseImageTar
	sandbox.BaseImageRef = baseImageRef
	sandbox.BaseImageRoot = baseImageRoot

	// bur doubles as its own bridge shims: inside a sandbox,
	// /run/bur/bin/{wl-paste,xclip,bur-pkg} link back to this binary.
	switch base := filepath.Base(os.Args[0]); base {
	case "wl-paste", "xclip":
		os.Exit(clipboard.RunShim(base, os.Args[1:], os.Stdout, os.Stderr))
	case "bur-pkg":
		os.Exit(pkgbridge.RunShim(os.Args[1:], os.Stdout, os.Stderr))
	}

	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Println(usage)
			return
		case "--version":
			fmt.Println("bur", version)
			return
		}
	}
	if err := run(args); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "bur:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Scaffolding writes files on the host; no container, so no podman.
	if len(args) > 0 && args[0] == "init" {
		return cmdInit(cwd, args[1:])
	}

	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman not found in PATH (bur runs everything in rootless podman; see https://podman.io/docs/installation)")
	}

	if len(args) > 0 {
		switch args[0] {
		case "exec":
			return cmdExec(cwd, args[1:])
		case "ls":
			return sandbox.Ls()
		case "clean":
			return cmdClean(len(args) > 1 && (args[1] == "-f" || args[1] == "--force"))
		}
	}

	cfg, root, warnings, err := config.Load(cwd)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "bur: warning:", w)
	}
	if err != nil {
		return err
	}
	if err := config.GuardRoot(root); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "bur: project", sandbox.AbbrevHome(root))
	name := sandbox.ContainerName(root)

	argv := cfg.Cmd
	if len(args) > 0 {
		argv = args
	}

	if err := sandbox.EnsureBaseImage(); err != nil {
		return err
	}

	// The env dir becomes /run/bur in the sandbox: env.sh (devshell),
	// plus the clipboard bridge's socket and shim binaries.
	envDir, err := os.MkdirTemp("", "bur-env-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(envDir)

	ref := nixenv.Resolve(root, cfg.NixShell)
	if ref != (nixenv.Ref{}) {
		script, err := nixenv.BuildEnv(ref, root)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(envDir, "env.sh"), script, 0o644); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "bur: note: no shell.nix or flake.nix found, starting with a minimal environment")
	}

	if cfg.Clipboard {
		if backend, note := clipboard.DetectBackend(); backend != nil {
			if err := clipboard.StartBridge(envDir, backend.Run); err != nil {
				fmt.Fprintln(os.Stderr, "bur: warning: clipboard bridge failed to start:", err)
			} else {
				// Agents gate clipboard code on the display env; the values
				// are cosmetic in the sandbox - the shims never touch a
				// display server. Explicit env config wins.
				for _, k := range []string{"WAYLAND_DISPLAY", "DISPLAY"} {
					if v, ok := os.LookupEnv(k); ok {
						if _, set := cfg.Env[k]; !set {
							cfg.Env[k] = v
						}
					}
				}
			}
		} else if note != "" {
			fmt.Fprintln(os.Stderr, "bur: note: clipboard paste disabled:", note)
		}
	}

	pkgAdd := false
	if cfg.NixPkgAdd {
		if _, err := exec.LookPath("nix"); err != nil {
			fmt.Fprintln(os.Stderr, "bur: note: bur-pkg disabled: nix not found on host PATH")
		} else if err := pkgbridge.StartBridge(envDir); err != nil {
			fmt.Fprintln(os.Stderr, "bur: warning: package bridge failed to start:", err)
		} else {
			pkgAdd = true
		}
	}
	if err := writeAgentMD(envDir, pkgAdd); err != nil {
		fmt.Fprintln(os.Stderr, "bur: warning: writing AGENT.md:", err)
	}

	var portMaps []ports.Mapping
	if cfg.Network == "none" {
		if len(cfg.Ports) > 0 {
			fmt.Fprintln(os.Stderr, "bur: warning: network is \"none\", configured ports will not be published")
		}
	} else {
		portMaps, err = ports.Allocate(cfg.Ports)
		if err != nil {
			return err
		}
		for i, p := range portMaps {
			_, host, _, _ := ports.ParseSpec(cfg.Ports[i])
			where := p.BindIP()
			if where == ports.DefaultPublishIP {
				where = "localhost"
			}
			if p.Host != host {
				fmt.Fprintf(os.Stderr, "bur: port %d taken, using %s:%d -> %d\n", host, where, p.Host, p.Container)
			} else {
				fmt.Fprintf(os.Stderr, "bur: %s:%d -> %d\n", where, p.Host, p.Container)
			}
			if p.BindIP() != ports.DefaultPublishIP {
				fmt.Fprintf(os.Stderr, "bur: warning: port %d is published on %s - reachable beyond localhost\n", p.Host, p.BindIP())
			}
		}
	}

	runArgs, err := sandbox.BuildRunArgs(sandbox.RunSpec{
		Name:    name,
		Root:    root,
		Workdir: cwd,
		EnvDir:  envDir,
		Cfg:     cfg,
		Ports:   portMaps,
	}, argv)
	if err != nil {
		return err
	}
	return sandbox.RunPodman(runArgs, sandbox.AgentHint(argv))
}

// cmdInit scaffolds the missing project files in the current directory;
// -f skips the repo-root prompt.
func cmdInit(cwd string, args []string) error {
	force := false
	for _, a := range args {
		switch a {
		case "-f", "--force":
			force = true
		default:
			return fmt.Errorf("usage: bur init [-f]")
		}
	}
	return config.Init(cwd, force)
}

// cmdClean sweeps both kinds of leftovers: orphaned containers (sandbox
// side) and devshell gc roots whose project no longer exists (nixenv side).
func cmdClean(force bool) error {
	removed, err := sandbox.Clean(force)
	if err != nil {
		return err
	}
	pruned, err := nixenv.PruneGCRoots()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bur: warning: pruning devshell gc roots:", err)
	}
	if pruned > 0 {
		fmt.Printf("pruned %d stale devshell gc root(s)\n", pruned)
	}
	if removed == 0 && pruned == 0 {
		fmt.Println("nothing to clean")
	}
	return nil
}

// agentMD lands at /run/bur/AGENT.md: orientation for the agent on what
// cage it is in and what the bridges offer.
const agentMD = `# bur sandbox

This shell runs inside bur, a one-shot rootless container. The project is
mounted read-write at its real path; the rest of the host is not visible.
The PATH comes from the project's nix devshell, and everything outside
the project directory vanishes when the sandbox exits.
`

const agentMDPkg = `
## Missing a tool?

Install extra tools from nixpkgs without leaving the sandbox:

    bur-pkg add <package> [<package>...]

Example: bur-pkg add imagemagick, then use magick. New tools land on PATH
immediately, in this very shell - no restart, no rebuild. They are fetched
by the host (this works even when the sandbox has no network) and are gone
when the sandbox exits. Package names are nixpkgs attribute names: try the
obvious name first, or search https://search.nixos.org/packages.
`

func writeAgentMD(envDir string, pkgAdd bool) error {
	doc := agentMD
	if pkgAdd {
		doc += agentMDPkg
	}
	return os.WriteFile(filepath.Join(envDir, "AGENT.md"), []byte(doc), 0o644)
}

// cmdExec targets a sandbox of the current project (same root walk as the
// config); --in <name> bypasses that for scripts and cross-project execs.
func cmdExec(cwd string, args []string) error {
	var target string
	if len(args) > 0 && args[0] == "--in" {
		if len(args) < 2 {
			return fmt.Errorf("usage: bur exec [--in <name>] <cmd> [args...]")
		}
		target, args = args[1], args[2:]
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: bur exec [--in <name>] <cmd> [args...]")
	}

	var name string
	if target != "" {
		all, err := sandbox.List()
		if err != nil {
			return err
		}
		if name, err = sandbox.MatchByName(all, target); err != nil {
			return err
		}
	} else {
		sb, err := sandbox.Resolve(config.FindProjectRoot(cwd), cwd)
		if err != nil {
			return err
		}
		name = sb.Name
	}
	return sandbox.RunPodman(sandbox.BuildExecArgs(name, args), sandbox.AgentHint(args))
}
