// Package pkgbridge lets a sandboxed agent pull extra tools from nixpkgs
// without any nix machinery entering the cage: the host side listens on
// a unix socket in the env dir, realizes requested packages with its own
// nix, and symlinks their bin entries into /run/bur/bin - already on the
// sandbox PATH, pointing into the live read-only /nix/store mount.
// Inside the sandbox, bur-pkg is bur re-exec'd in shim mode.
//
// Security rests on two facts: names resolve only against the host's own
// nixpkgs registry, never against anything the sandbox can write to (the
// project's flake.lock would let the agent pick what the host evaluates),
// and the host never runs what it builds.
package pkgbridge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jeliasson/bur/internal/shim"
)

// Version is stamped by main at startup; the shim prints it in its
// --version output.
var Version = "dev"

const (
	sockName = "pkg.sock"
	sockPath = "/run/bur/" + sockName // as seen inside the sandbox

	reqAdd = "ADD"

	// Substituted packages arrive in seconds; the ceiling is for the
	// occasional source build.
	buildTimeout = 15 * time.Minute
	maxNameLen   = 120
)

// nameRE allows a bare attribute path ("imagemagick",
// "python3Packages.pillow") and nothing that could smuggle a flag, flake
// URI, or ../ segment - the name becomes an argv element and the
// gc-root file name.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// AddFunc realizes one package and returns the tool names now on the
// sandbox PATH.
type AddFunc func(name string) ([]string, error)

// StartBridge installs the bur-pkg shim in the env dir and serves
// requests until the process exits.
func StartBridge(envDir string) error {
	if err := shim.Install(filepath.Join(envDir, "bin"), "bur-pkg"); err != nil {
		return err
	}
	add := func(name string) ([]string, error) { return realize(envDir, name) }
	return serveSocket(filepath.Join(envDir, sockName), add)
}

// realize builds nixpkgs#name on the host and links its bin entries into
// the env dir. The --out-link doubles as a GC root that dangles once the
// env dir is removed on exit, so the next nix GC sweeps the package away.
func realize(envDir, name string) ([]string, error) {
	rootsDir := filepath.Join(envDir, "pkgroots")
	if err := os.MkdirAll(rootsDir, 0o755); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix",
		"--extra-experimental-features", "nix-command flakes",
		"build", "--out-link", filepath.Join(rootsDir, name),
		"--print-out-paths", "--", "nixpkgs#"+name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("building %s timed out after %s", name, buildTimeout)
		}
		if msg := errorLine(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	var tools []string
	for _, out := range strings.Fields(stdout.String()) {
		linked, err := linkBins(out, filepath.Join(envDir, "bin"))
		if err != nil {
			return nil, err
		}
		tools = append(tools, linked...)
	}
	sort.Strings(tools)
	return tools, nil
}

// linkBins symlinks out/bin entries into binDir, refusing to shadow
// names already taken (the clipboard shims live there). Returns the
// entries reachable through this package, re-adds included.
func linkBins(out, binDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(out, "bin"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // a library: nothing to put on PATH
		}
		return nil, err
	}
	var tools []string
	for _, e := range entries {
		dest := filepath.Join(out, "bin", e.Name())
		err := os.Symlink(dest, filepath.Join(binDir, e.Name()))
		if os.IsExist(err) {
			if cur, lerr := os.Readlink(filepath.Join(binDir, e.Name())); lerr == nil && cur == dest {
				tools = append(tools, e.Name())
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		tools = append(tools, e.Name())
	}
	return tools, nil
}

// errorLine digs the useful line out of nix's stderr: the last one
// mentioning "error:", else the last non-empty one.
func errorLine(s string) string {
	pick := ""
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pick == "" || strings.Contains(line, "error:") {
			pick = line
		}
	}
	if len(pick) > 200 {
		pick = pick[:200]
	}
	return pick
}

func serveSocket(path string, add AddFunc) error {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// keep-id maps the sandbox to the same uid, so owner-only suffices.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, add)
		}
	}()
	return nil
}

func serveConn(conn net.Conn, add AddFunc) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, 1024)).ReadString('\n')
	if err != nil {
		return
	}
	name, ok := parseRequest(strings.TrimRight(line, "\n"))
	if !ok {
		reply(conn, "ERR bad request")
		return
	}
	if len(name) > maxNameLen || !nameRE.MatchString(name) {
		reply(conn, fmt.Sprintf("ERR %q is not a nixpkgs attribute name", name))
		return
	}
	tools, err := add(name)
	if err != nil {
		reply(conn, "ERR "+oneLine(err.Error()))
		return
	}
	reply(conn, strings.TrimRight("OK "+strings.Join(tools, " "), " "))
}

func reply(conn net.Conn, line string) {
	conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	fmt.Fprintf(conn, "%s\n", line)
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func parseRequest(line string) (name string, ok bool) {
	name, ok = strings.CutPrefix(line, reqAdd+" ")
	return name, ok && name != ""
}

// --- sandbox side: bur running as bur-pkg ---

const usage = `usage: bur-pkg add <package> [<package>...]

Install tools from nixpkgs into this sandbox. Packages are fetched by
the host and land on PATH immediately, in already-running shells too;
everything vanishes when the sandbox exits. Names are nixpkgs attribute
names - try the obvious one, or search https://search.nixos.org/packages`

// RunShim is the shim entry point: bur invoked as bur-pkg inside the
// sandbox. It returns the process exit code.
func RunShim(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "-v", "--version":
			fmt.Fprintf(stdout, "bur-pkg (bur package bridge %s)\n", Version)
			return 0
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, usage)
			return 0
		}
	}
	if len(args) < 2 || args[0] != "add" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sock := os.Getenv("BUR_PKG_SOCK")
	if sock == "" {
		sock = sockPath
	}
	code := 0
	for _, name := range args[1:] {
		fmt.Fprintf(stderr, "bur-pkg: fetching %s via the host ...\n", name)
		tools, err := shimRequest(sock, name)
		if err != nil {
			fmt.Fprintf(stderr, "bur-pkg: %s: %s\n", name, err)
			code = 1
			continue
		}
		if len(tools) == 0 {
			fmt.Fprintf(stdout, "%s realized, but it has no bin/ - nothing new on PATH\n", name)
		} else {
			fmt.Fprintf(stdout, "%s: now on PATH: %s\n", name, strings.Join(tools, ", "))
		}
	}
	return code
}

func shimRequest(sock, name string) ([]string, error) {
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("bridge socket unavailable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(buildTimeout + time.Minute))
	if _, err := conn.Write([]byte(reqAdd + " " + name + "\n")); err != nil {
		return nil, err
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return nil, err
	}
	status = strings.TrimRight(status, "\n")
	if msg, isErr := strings.CutPrefix(status, "ERR "); isErr {
		return nil, errors.New(msg)
	}
	if rest, isOK := strings.CutPrefix(status, "OK"); isOK {
		return strings.Fields(rest), nil
	}
	return nil, fmt.Errorf("unexpected bridge reply %q", status)
}
