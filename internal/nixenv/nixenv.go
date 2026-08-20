// Package nixenv resolves which nix devshell a project uses and builds
// it on the host into a sourceable environment script.
package nixenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// Ref is where the devshell comes from: a nix file (shell.nix style,
// built impurely) or a flake installable (pure, pinned by the project's
// flake.lock). Exactly one field is set; the zero value means the
// project has no devshell.
type Ref struct {
	File  string // path to a nix expression, "" in flake mode
	Flake string // flake installable like "." or ".#dev", "" in file mode
}

func (r Ref) String() string {
	if r.File != "" {
		return r.File
	}
	return "flake " + r.Flake
}

// Resolve picks the devshell source: nixShell from config (a flake
// installable when it contains '#', a file path otherwise), else
// shell.nix, else the project flake's default devShell.
func Resolve(root, nixShell string) Ref {
	if nixShell != "" {
		s := nixShell
		if strings.Contains(s, "#") {
			return Ref{Flake: s}
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(root, s)
		}
		return Ref{File: s}
	}
	if _, err := os.Stat(filepath.Join(root, "shell.nix")); err == nil {
		return Ref{File: filepath.Join(root, "shell.nix")}
	}
	if _, err := os.Stat(filepath.Join(root, "flake.nix")); err == nil {
		// "." and not "path:.": path would copy the whole worktree
		// (node_modules and all) into the store on every start.
		return Ref{Flake: "."}
	}
	return Ref{}
}

// BuildEnv builds the project devshell on the host and returns the
// sourceable environment script. The --profile link doubles as a GC root
// so nix-collect-garbage cannot remove store paths under a sandbox.
func BuildEnv(ref Ref, root string) ([]byte, error) {
	gcroots, err := gcrootsDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(gcroots, 0o755); err != nil {
		return nil, err
	}
	profile := filepath.Join(gcroots, shortHash(root))

	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"print-dev-env", "--profile", profile,
	}
	if ref.File != "" {
		args = append(args, "--impure", "-f", ref.File)
	} else {
		args = append(args, ref.Flake)
	}
	cmd := exec.Command("nix", args...)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("building devshell from %s failed: %w", ref, err)
	}
	// The sidecar records which project the profile pins - the hash is
	// one-way, and PruneGCRoots needs the path to see the project is gone.
	if err := os.WriteFile(profile+".root", []byte(root+"\n"), 0o644); err != nil {
		return nil, err
	}
	return out, nil
}

func gcrootsDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "bur", "gcroots"), nil
}

// PruneGCRoots drops the devshell profiles of projects that no longer
// exist, so their store paths become garbage-collectable again. Returns
// how many projects were pruned.
func PruneGCRoots() (int, error) {
	dir, err := gcrootsDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	pruned := 0
	for _, e := range entries {
		hash, ok := strings.CutSuffix(e.Name(), ".root")
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		root := strings.TrimSpace(string(data))
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); err == nil {
			continue // project still exists, keep its root
		}
		// The profile link, its generations, and the sidecar all share
		// the hash prefix; removing a live project's root by accident
		// would only cost a devshell rebuild, but the stat above keeps
		// even that from happening.
		links, _ := filepath.Glob(filepath.Join(dir, hash+"*"))
		for _, l := range links {
			os.Remove(l)
		}
		pruned++
	}
	return pruned, nil
}
