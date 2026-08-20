package clipboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeAllowlist(t *testing.T) {
	allowed := []string{
		"image/png", "image/jpeg", "image/bmp", "image/webp",
		"text/plain", "text/plain;charset=utf-8", "text/html",
		"UTF8_STRING", "STRING", "TEXT",
	}
	for _, typ := range allowed {
		if !typeRE.MatchString(typ) {
			t.Errorf("type %q should be allowed", typ)
		}
	}
	denied := []string{
		"", "application/pdf", "x-special/gnome-copied-files",
		"image/png -o", "--watch", "-t", "text/..", "image/",
		"text/plain;charset=utf 8", "TARGETS", "text/plain\nGET",
	}
	for _, typ := range denied {
		if typeRE.MatchString(typ) {
			t.Errorf("type %q should be denied", typ)
		}
	}
}

func TestParseWlPasteArgs(t *testing.T) {
	cases := []struct {
		args []string
		want shimReq
		err  bool
	}{
		// the exact invocations claude uses
		{[]string{}, shimReq{kind: reqGet, newline: true}, false},
		{[]string{"-l"}, shimReq{kind: reqTypes, newline: true}, false},
		{[]string{"--type", "image/png"}, shimReq{kind: reqGet, typ: "image/png", newline: true}, false},
		{[]string{"--type", "image/bmp"}, shimReq{kind: reqGet, typ: "image/bmp", newline: true}, false},

		{[]string{"-n", "-t", "text/plain"}, shimReq{kind: reqGet, typ: "text/plain"}, false},
		{[]string{"--type=text/plain"}, shimReq{kind: reqGet, typ: "text/plain", newline: true}, false},
		{[]string{"--list-types"}, shimReq{kind: reqTypes, newline: true}, false},
		{[]string{"--primary"}, shimReq{}, true},
		{[]string{"-p"}, shimReq{}, true},
		{[]string{"--watch", "cat"}, shimReq{}, true},
		{[]string{"-t"}, shimReq{}, true},
		{[]string{"--bogus"}, shimReq{}, true},
	}
	for _, c := range cases {
		got, err := parseWlPasteArgs(c.args)
		if c.err != (err != nil) {
			t.Errorf("wl-paste %v: err = %v, want err = %v", c.args, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("wl-paste %v = %+v, want %+v", c.args, got, c.want)
		}
	}
	if _, err := parseWlPasteArgs([]string{"-v"}); err != errVersion {
		t.Errorf("wl-paste -v: err = %v, want errVersion", err)
	}
}

func TestParseXclipArgs(t *testing.T) {
	cases := []struct {
		args []string
		want shimReq
		err  bool
	}{
		// the exact invocations claude uses
		{[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, shimReq{kind: reqTypes}, false},
		{[]string{"-selection", "clipboard", "-t", "image/png", "-o"}, shimReq{kind: reqGet, typ: "image/png"}, false},
		{[]string{"-selection", "clipboard", "-t", "text/plain", "-o"}, shimReq{kind: reqGet, typ: "text/plain"}, false},

		{[]string{"-se", "clip", "-o"}, shimReq{kind: reqGet}, false},
		{[]string{"-o", "-r"}, shimReq{kind: reqGet, rmLastNL: true}, false},
		{[]string{"-d", ":0", "-o"}, shimReq{kind: reqGet}, false},
		{[]string{"-selection", "clipboard"}, shimReq{}, true},       // write mode
		{[]string{"-i", "-selection", "clipboard"}, shimReq{}, true}, // write mode
		{[]string{"-selection", "primary", "-o"}, shimReq{}, true},
		{[]string{"-o", "-bogus"}, shimReq{}, true},
	}
	for _, c := range cases {
		got, err := parseXclipArgs(c.args)
		if c.err != (err != nil) {
			t.Errorf("xclip %v: err = %v, want err = %v", c.args, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("xclip %v = %+v, want %+v", c.args, got, c.want)
		}
	}
	if _, err := parseXclipArgs([]string{"-version"}); err != errVersion {
		t.Errorf("xclip -version: err = %v, want errVersion", err)
	}
}

// shortTempDir avoids unix socket path length limits (~108 bytes) when
// the test runner's TMPDIR is deeply nested.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "burclip")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestBridgeRoundTrip(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	run := func(kind, typ string) ([]byte, error) {
		switch {
		case kind == reqTypes:
			return []byte("TARGETS\nimage/png\nUTF8_STRING\n"), nil
		case typ == "image/png":
			return pngBytes, nil
		case typ == "" || strings.HasPrefix(typ, "text/"):
			return []byte("hello"), nil
		}
		return nil, errors.New("target not available")
	}

	dir := shortTempDir(t)
	sock := filepath.Join(dir, sockName)
	if err := serveSocket(sock, run); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUR_CLIP_SOCK", sock)

	shim := func(tool string, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := RunShim(tool, args, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	// binary image content passes through untouched, no newline games
	code, out, _ := shim("wl-paste", "--type", "image/png")
	if code != 0 || out != string(pngBytes) {
		t.Errorf("wl-paste image: code %d, out %q", code, out)
	}
	code, out, _ = shim("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	if code != 0 || out != string(pngBytes) {
		t.Errorf("xclip image: code %d, out %q", code, out)
	}

	// wl-paste appends a newline to text unless -n; xclip never does
	if _, out, _ = shim("wl-paste"); out != "hello\n" {
		t.Errorf("wl-paste text = %q, want %q", out, "hello\n")
	}
	if _, out, _ = shim("wl-paste", "-n"); out != "hello" {
		t.Errorf("wl-paste -n text = %q, want %q", out, "hello")
	}
	if _, out, _ = shim("xclip", "-selection", "clipboard", "-t", "text/plain", "-o"); out != "hello" {
		t.Errorf("xclip text = %q, want %q", out, "hello")
	}

	// type listing via either tool's spelling
	if _, out, _ = shim("wl-paste", "-l"); !strings.Contains(out, "image/png") {
		t.Errorf("wl-paste -l = %q", out)
	}
	if _, out, _ = shim("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"); !strings.Contains(out, "image/png") {
		t.Errorf("xclip TARGETS = %q", out)
	}

	// the server refuses non text/image types before running anything
	code, _, errOut := shim("wl-paste", "--type", "application/pdf")
	if code != 1 || !strings.Contains(errOut, "not bridged") {
		t.Errorf("denied type: code %d, stderr %q", code, errOut)
	}

	// backend failures surface as tool-style errors, exit 1
	code, _, errOut = shim("wl-paste", "--type", "image/webp")
	if code != 1 || !strings.Contains(errOut, "target not available") {
		t.Errorf("backend error: code %d, stderr %q", code, errOut)
	}

	// a dead socket fails cleanly - the agent's || chains move on
	t.Setenv("BUR_CLIP_SOCK", filepath.Join(dir, "nope.sock"))
	if code, _, _ = shim("wl-paste"); code != 1 {
		t.Errorf("dead socket: code %d, want 1", code)
	}
}

func TestStartBridgeInstallsShims(t *testing.T) {
	dir := shortTempDir(t)
	run := func(kind, typ string) ([]byte, error) { return []byte("x"), nil }
	if err := StartBridge(dir, run); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wl-paste", "xclip"} {
		p := filepath.Join(dir, "bin", name)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("shim %s: %v", name, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("shim %s is not a symlink", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, sockName)); err != nil {
		t.Errorf("socket: %v", err)
	}
}

func TestParseRequest(t *testing.T) {
	cases := []struct {
		line, kind, typ string
		ok              bool
	}{
		{"GET", reqGet, "", true},
		{"GET image/png", reqGet, "image/png", true},
		{"TYPES", reqTypes, "", true},
		{"GET ", "", "", false},
		{"PUT image/png", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		kind, typ, ok := parseRequest(c.line)
		if ok != c.ok || (ok && (kind != c.kind || typ != c.typ)) {
			t.Errorf("parseRequest(%q) = %q %q %v, want %q %q %v",
				c.line, kind, typ, ok, c.kind, c.typ, c.ok)
		}
	}
}
