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
  bur clean [-f]   remove all bur containers and stale devshell gc roots (asks first)
  bur --version    print the bur version

A sandbox lives exactly as long as its command: exit the agent and the
container is gone. Run as many per project as you like.

Config: ~/.config/bur/config.yaml (global), .bur.yaml (project).
Keys: cmd, tools, mounts, ports, env, network (open|none), hostAccess,
      clipboard, nix.shell`

func main() {
	clipboard.Version = version
	sandbox.BaseImageTar = baseImageTar
	sandbox.BaseImageRef = baseImageRef
	sandbox.BaseImageRoot = baseImageRoot

	// bur doubles as its own clipboard shim: inside a sandbox,
	// /run/bur/bin/{wl-paste,xclip} link back to this binary.
	if base := filepath.Base(os.Args[0]); base == "wl-paste" || base == "xclip" {
		os.Exit(clipboard.RunShim(base, os.Args[1:], os.Stdout, os.Stderr))
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
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman not found in PATH (bur runs everything in rootless podman; see https://podman.io/docs/installation)")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
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
