// Package clipboard bridges host clipboard reads into the sandbox, so an
// agent can paste host content without the compositor socket entering the
// container: the host listens on a unix socket in the env dir and runs the
// real tool, while /run/bur/bin holds wl-paste and xclip names that are bur
// re-exec'd in shim mode - agents read the clipboard through exactly these
// two tools. Read-only by design: an agent that can seed the host clipboard
// can poison the user's next paste into a terminal.
package clipboard

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
	"strings"
	"time"

	"github.com/jeliasson/bur/internal/bridge"
	"github.com/jeliasson/bur/internal/shim"
)

// Version is stamped by main at startup.
var Version = "dev"

const (
	sockName = "clip.sock"
	sockPath = "/run/bur/" + sockName // as seen inside the sandbox

	reqGet   = "GET"
	reqTypes = "TYPES"
)

// typeRE bounds what the sandbox may ask the host clipboard for:
// text and image MIME types, plus the X11 text atoms xclip accepts. The
// value only ever becomes an argv element (never a shell word), but the
// tight charset also keeps it from smuggling option flags.
var typeRE = regexp.MustCompile(`^(?:(?:text|image)/[A-Za-z0-9][A-Za-z0-9.+_-]*(?:;charset=[A-Za-z0-9._-]+)?|UTF8_STRING|STRING|TEXT)$`)

type RunFunc func(kind, typ string) ([]byte, error)

type Backend struct {
	tool string // "wl-paste" or "xclip"
}

// DetectBackend prefers Wayland when both displays are up (XWayland). No
// display at all is a headless host, so it stays silent instead of noting.
func DetectBackend() (*Backend, string) {
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	x11 := os.Getenv("DISPLAY") != ""
	if wayland {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return &Backend{tool: "wl-paste"}, ""
		}
	}
	if x11 {
		if _, err := exec.LookPath("xclip"); err == nil {
			return &Backend{tool: "xclip"}, ""
		}
	}
	switch {
	case wayland:
		return nil, "wl-paste not found on host PATH (install wl-clipboard)"
	case x11:
		return nil, "xclip not found on host PATH"
	}
	return nil, ""
}

// Run keeps a fixed argv shape: typ is a validated value slot, so nothing
// from the sandbox ever becomes an option or a shell word. Text is fetched
// raw (--no-newline); the shim owns newline cosmetics.
func (b *Backend) Run(kind, typ string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var argv []string
	if b.tool == "wl-paste" {
		if kind == reqTypes {
			argv = []string{"wl-paste", "--list-types"}
		} else {
			argv = []string{"wl-paste", "--no-newline"}
			if typ != "" {
				argv = append(argv, "--type", typ)
			}
		}
	} else {
		if kind == reqTypes {
			argv = []string{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"}
		} else {
			argv = []string{"xclip", "-selection", "clipboard", "-o"}
			if typ != "" {
				argv = append(argv, "-t", typ)
			}
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := firstLine(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func StartBridge(envDir string, run RunFunc) error {
	if err := shim.Install(filepath.Join(envDir, "bin"), "wl-paste", "xclip"); err != nil {
		return err
	}
	return serveSocket(filepath.Join(envDir, sockName), run)
}

func serveSocket(path string, run RunFunc) error {
	return bridge.Listen(path, func(conn net.Conn) { serveConn(conn, run) })
}

func serveConn(conn net.Conn, run RunFunc) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(conn, 1024)).ReadString('\n')
	if err != nil {
		return
	}
	kind, typ, ok := parseRequest(strings.TrimRight(line, "\n"))
	conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	if !ok {
		fmt.Fprintf(conn, "ERR bad request\n")
		return
	}
	if typ != "" && !typeRE.MatchString(typ) {
		fmt.Fprintf(conn, "ERR type %q is not bridged (text/* and image/* only)\n", typ)
		return
	}
	out, err := run(kind, typ)
	if err != nil {
		fmt.Fprintf(conn, "ERR %s\n", firstLine(err.Error()))
		return
	}
	conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	if _, err := conn.Write([]byte("OK\n")); err != nil {
		return
	}
	conn.Write(out)
}

func parseRequest(line string) (kind, typ string, ok bool) {
	switch {
	case line == reqTypes:
		return reqTypes, "", true
	case line == reqGet:
		return reqGet, "", true
	case strings.HasPrefix(line, reqGet+" "):
		typ = strings.TrimPrefix(line, reqGet+" ")
		return reqGet, typ, typ != ""
	}
	return "", "", false
}

// --- sandbox side: bur running as wl-paste/xclip ---

type shimReq struct {
	kind     string
	typ      string
	newline  bool // wl-paste: append \n to text output (default on)
	rmLastNL bool // xclip -rmlastnl: strip one trailing \n
}

// errVersion short-circuits option parsing for -version/--help style
// flags: print an identifying line and exit 0, like the real tools.
var errVersion = errors.New("version request")

// RunShim is the sandbox-side entry point and returns the process exit code.
func RunShim(tool string, args []string, stdout, stderr io.Writer) int {
	req, err := parseShimArgs(tool, args)
	if err == errVersion {
		fmt.Fprintf(stdout, "%s (bur clipboard bridge %s)\n", tool, Version)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s (bur clipboard bridge): %s\n", tool, err)
		return 1
	}
	sock := os.Getenv("BUR_CLIP_SOCK")
	if sock == "" {
		sock = sockPath
	}
	out, err := shimRequest(sock, req.kind, req.typ)
	if err != nil {
		fmt.Fprintf(stderr, "%s (bur clipboard bridge): %s\n", tool, err)
		return 1
	}
	if req.rmLastNL {
		out = bytes.TrimSuffix(out, []byte("\n"))
	}
	stdout.Write(out)
	if req.newline && req.kind == reqGet && len(out) > 0 &&
		(req.typ == "" || strings.HasPrefix(req.typ, "text/")) &&
		!bytes.HasSuffix(out, []byte("\n")) {
		stdout.Write([]byte("\n"))
	}
	return 0
}

func shimRequest(sock, kind, typ string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("bridge socket unavailable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	req := kind
	if typ != "" {
		req += " " + typ
	}
	if _, err := conn.Write([]byte(req + "\n")); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	status = strings.TrimRight(status, "\n")
	if msg, isErr := strings.CutPrefix(status, "ERR "); isErr {
		return nil, errors.New(msg)
	}
	if status != "OK" {
		return nil, fmt.Errorf("unexpected bridge reply %q", status)
	}
	return io.ReadAll(r)
}

func parseShimArgs(tool string, args []string) (shimReq, error) {
	if tool == "wl-paste" {
		return parseWlPasteArgs(args)
	}
	return parseXclipArgs(args)
}

func parseWlPasteArgs(args []string) (shimReq, error) {
	req := shimReq{kind: reqGet, newline: true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("option %s needs a value", a)
			}
			return args[i], nil
		}
		switch {
		case a == "-n" || a == "--no-newline":
			req.newline = false
		case a == "-l" || a == "--list-types":
			req.kind = reqTypes
		case a == "-t" || a == "--type":
			v, err := value()
			if err != nil {
				return req, err
			}
			req.typ = v
		case strings.HasPrefix(a, "--type="):
			req.typ = strings.TrimPrefix(a, "--type=")
		case a == "-p" || a == "--primary":
			return req, errors.New("the primary selection is not bridged")
		case a == "-w" || a == "--watch":
			return req, errors.New("watch mode is not bridged")
		case a == "-s" || a == "--seat":
			if _, err := value(); err != nil {
				return req, err
			}
		case strings.HasPrefix(a, "--seat="):
			// ignored
		case a == "-v" || a == "--version" || a == "-h" || a == "--help":
			return req, errVersion
		default:
			return req, fmt.Errorf("unsupported option %q", a)
		}
	}
	return req, nil
}

// xclipFlag matches xclip's unique-prefix option style: arg must be a
// prefix of the full option name and long enough to be unambiguous among
// the options handled here.
func xclipFlag(arg, full string, min int) bool {
	return len(arg) >= min && strings.HasPrefix(full, arg)
}

func parseXclipArgs(args []string) (shimReq, error) {
	req := shimReq{kind: reqGet}
	out := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("option %s needs a value", a)
			}
			return args[i], nil
		}
		switch {
		case xclipFlag(a, "-out", 2): // -o
			out = true
		case xclipFlag(a, "-in", 2): // -i, the default mode anyway
		case xclipFlag(a, "-selection", 3): // -se, vs -si for -silent
			v, err := value()
			if err != nil {
				return req, err
			}
			if !strings.HasPrefix("clipboard", v) || v == "" {
				return req, fmt.Errorf("only the clipboard selection is bridged, not %q", v)
			}
		case xclipFlag(a, "-target", 2): // -t
			v, err := value()
			if err != nil {
				return req, err
			}
			req.typ = v
		case xclipFlag(a, "-display", 2): // -d, irrelevant in the sandbox
			if _, err := value(); err != nil {
				return req, err
			}
		case xclipFlag(a, "-loops", 2): // -l
			if _, err := value(); err != nil {
				return req, err
			}
		case xclipFlag(a, "-rmlastnl", 2): // -r
			req.rmLastNL = true
		case xclipFlag(a, "-quiet", 2), xclipFlag(a, "-silent", 3),
			xclipFlag(a, "-verbose", 4), xclipFlag(a, "-noutf8", 2):
			// ignored
		case a == "-version", xclipFlag(a, "-help", 2):
			return req, errVersion
		default:
			return req, fmt.Errorf("unsupported option %q", a)
		}
	}
	if !out {
		return req, errors.New("only reads (-o) are bridged; clipboard writes stay outside the cage")
	}
	if req.typ == "TARGETS" {
		req.kind = reqTypes
		req.typ = ""
	}
	return req, nil
}
