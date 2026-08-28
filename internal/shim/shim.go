// Package shim installs sandbox-side command names that re-exec bur:
// main dispatches on argv[0], so a symlink named wl-paste or bur-pkg in
// /run/bur/bin becomes that tool inside the sandbox.
package shim

import (
	"os"
	"path/filepath"
	"strings"
)

// Install populates binDir with the given names, idempotently so each
// bridge can bring its own. A nix-installed bur is already mounted into
// the sandbox, so a symlink suffices; other builds are copied in once as
// "bur" - and had better be static, which CGO_ENABLED=0 dev builds are.
func Install(binDir string, names ...string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return err
	}
	target := self
	if !strings.HasPrefix(self, "/nix/store/") {
		copied := filepath.Join(binDir, "bur")
		if _, err := os.Stat(copied); err != nil {
			data, err := os.ReadFile(self)
			if err != nil {
				return err
			}
			if err := os.WriteFile(copied, data, 0o755); err != nil {
				return err
			}
		}
		target = "bur" // relative link, resolves inside the mount
	}
	for _, name := range names {
		if err := os.Symlink(target, filepath.Join(binDir, name)); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}
