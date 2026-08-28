package pkgbridge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNameRE(t *testing.T) {
	valid := []string{
		"imagemagick", "python3Packages.pillow", "gcc13", "wl-clipboard",
		"nodejs_22", "_7zz", "ffmpeg-full", "a",
	}
	for _, name := range valid {
		if !nameRE.MatchString(name) {
			t.Errorf("nameRE rejects valid name %q", name)
		}
	}
	invalid := []string{
		"", "-rebuild", ".hidden", "../etc", "a/b", "a b", "a#b",
		"github:evil/repo", "nixpkgs#foo", "a;b", "a$(x)", "--impure",
		"path:./evil", "a\nb",
	}
	for _, name := range invalid {
		if nameRE.MatchString(name) {
			t.Errorf("nameRE accepts invalid name %q", name)
		}
	}
}

func TestParseRequest(t *testing.T) {
	cases := []struct {
		line, name string
		ok         bool
	}{
		{"ADD imagemagick", "imagemagick", true},
		{"ADD ", "", false},
		{"ADD", "", false},
		{"GET foo", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		name, ok := parseRequest(c.line)
		if ok != c.ok || (ok && name != c.name) {
			t.Errorf("parseRequest(%q) = %q, %v; want %q, %v", c.line, name, ok, c.name, c.ok)
		}
	}
}

// fakeOut builds a pretend store output with the given bin entries.
func fakeOut(t *testing.T, dir string, bins ...string) string {
	t.Helper()
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(out, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, b := range bins {
		if err := os.WriteFile(filepath.Join(out, "bin", b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestLinkBins(t *testing.T) {
	dir := t.TempDir()
	out := fakeOut(t, dir, "magick", "convert")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An existing foreign entry (a clipboard shim, say) must survive.
	if err := os.Symlink("/elsewhere/wl-paste", filepath.Join(binDir, "convert")); err != nil {
		t.Fatal(err)
	}

	tools, err := linkBins(out, binDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0] != "magick" {
		t.Fatalf("tools = %v, want [magick]", tools)
	}
	if got, _ := os.Readlink(filepath.Join(binDir, "convert")); got != "/elsewhere/wl-paste" {
		t.Errorf("existing entry was clobbered: now points at %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(binDir, "magick")); got != filepath.Join(out, "bin", "magick") {
		t.Errorf("magick points at %q", got)
	}

	// Re-adding the same package reports its tools as available again.
	tools, err = linkBins(out, binDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0] != "magick" {
		t.Fatalf("second add: tools = %v, want [magick]", tools)
	}
}

func TestLinkBinsNoBinDir(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil { // a library: no bin/
		t.Fatal(err)
	}
	tools, err := linkBins(out, filepath.Join(dir, "bin"))
	if err != nil || tools != nil {
		t.Fatalf("linkBins on library = %v, %v; want nil, nil", tools, err)
	}
}

func TestErrorLine(t *testing.T) {
	s := "warning: something\nerror: attribute 'nope' missing\n\n"
	if got := errorLine(s); got != "error: attribute 'nope' missing" {
		t.Errorf("errorLine = %q", got)
	}
	if got := errorLine("just noise\n"); got != "just noise" {
		t.Errorf("errorLine fallback = %q", got)
	}
	if got := errorLine(""); got != "" {
		t.Errorf("errorLine empty = %q", got)
	}
}

// TestBridgeEndToEnd runs the socket protocol shim<->server with a
// stubbed AddFunc.
func TestBridgeEndToEnd(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "pkg.sock")
	add := func(name string) ([]string, error) {
		switch name {
		case "imagemagick":
			return []string{"magick", "convert"}, nil
		case "sqlite":
			return nil, nil
		default:
			return nil, errors.New("attribute missing")
		}
	}
	if err := serveSocket(sock, add); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUR_PKG_SOCK", sock)

	var stdout, stderr bytes.Buffer
	if code := RunShim([]string{"add", "imagemagick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if want := "imagemagick: now on PATH: magick, convert\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}

	stdout.Reset()
	stderr.Reset()
	if code := RunShim([]string{"add", "sqlite"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no bin/") {
		t.Errorf("stdout = %q, want a no-bin note", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := RunShim([]string{"add", "nope"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "attribute missing") {
		t.Errorf("stderr = %q, want the server error", stderr.String())
	}

	// Validation happens server-side, before the AddFunc runs.
	stdout.Reset()
	stderr.Reset()
	if code := RunShim([]string{"add", "github:evil/repo"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not a nixpkgs attribute name") {
		t.Errorf("stderr = %q, want a validation error", stderr.String())
	}
}

// fakeNix "realizes" the requested package into $FAKE_STORE, honors
// --out-link, and prints the out path - the realize contract without nix.
const fakeNix = `#!/bin/sh
link="" pkg="" next=""
for a in "$@"; do
	if [ "$next" = link ]; then link="$a"; next=""; continue; fi
	case "$a" in
	--out-link) next=link ;;
	nixpkgs#*) pkg="${a#nixpkgs#}" ;;
	esac
done
if [ "$pkg" = nope ]; then
	echo "warning: some noise" >&2
	echo "error: flake 'nixpkgs' does not provide attribute 'nope'" >&2
	exit 1
fi
out="$FAKE_STORE/$pkg"
mkdir -p "$out/bin"
printf '#!/bin/sh\n' > "$out/bin/$pkg"
chmod +x "$out/bin/$pkg"
ln -s "$out" "$link"
echo "$out"
`

// TestBridgeWithFakeNix drives the full chain with only nix faked.
func TestBridgeWithFakeNix(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "nix"), []byte(fakeNix), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_STORE", filepath.Join(dir, "store"))

	envDir := filepath.Join(dir, "env")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := StartBridge(envDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUR_PKG_SOCK", filepath.Join(envDir, sockName))

	var stdout, stderr bytes.Buffer
	if code := RunShim([]string{"add", "hello"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if want := "hello: now on PATH: hello\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	link, err := os.Readlink(filepath.Join(envDir, "bin", "hello"))
	if err != nil || link != filepath.Join(dir, "store", "hello", "bin", "hello") {
		t.Errorf("bin/hello -> %q, %v", link, err)
	}
	// The out-link gc root lives under the env dir and dies with it.
	if _, err := os.Readlink(filepath.Join(envDir, "pkgroots", "hello")); err != nil {
		t.Errorf("pkgroots/hello: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := RunShim([]string{"add", "nope"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "does not provide attribute 'nope'") {
		t.Errorf("stderr = %q, want nix's error line", stderr.String())
	}
}

func TestShimUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunShim(nil, &stdout, &stderr); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
	if code := RunShim([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Errorf("--help: exit %d, want 0", code)
	}
	if code := RunShim([]string{"add"}, &stdout, &stderr); code != 2 {
		t.Errorf("add with no names: exit %d, want 2", code)
	}
	stdout.Reset()
	if code := RunShim([]string{"--version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "bur-pkg") {
		t.Errorf("--version: exit %d, stdout %q", code, stdout.String())
	}
}
