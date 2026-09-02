package sandbox

import (
	"fmt"
	"os"
	"strings"

	"github.com/jeliasson/bur/internal/config"
)

// Container-side paths; the host files live in the env dir mounted at /run/bur.
const (
	gitConfigPath  = "/run/bur/gitconfig"
	signingKeyPath = "/run/bur/signing_key"
)

// writeGitConfig renders the sandbox's global gitconfig into the env dir.
// The host git config is never mounted: identity comes from bur config, and
// signing stays off unless a dedicated key is configured - inherited signing
// settings would otherwise fail every commit on a key that is not there.
func writeGitConfig(envDir string, cfg config.Config) error {
	var b strings.Builder
	b.WriteString("[user]\n")
	fmt.Fprintf(&b, "\tname = %s\n", gitConfigValue(cfg.GitName))
	fmt.Fprintf(&b, "\temail = %s\n", gitConfigValue(cfg.GitEmail))
	if cfg.GitSigningKey != "" {
		fmt.Fprintf(&b, "\tsigningkey = %s\n", signingKeyPath)
		b.WriteString("[gpg]\n\tformat = ssh\n")
		b.WriteString("[commit]\n\tgpgsign = true\n")
	} else {
		b.WriteString("[commit]\n\tgpgsign = false\n")
	}
	b.WriteString("[init]\n\tdefaultBranch = main\n")
	// Empty value resets the helper list: nothing in the cage should ever
	// prompt for or store credentials.
	b.WriteString("[credential]\n\thelper = \n")
	return os.WriteFile(envDir+"/gitconfig", []byte(b.String()), 0o644)
}

// gitConfigValue quotes a value for git's config syntax. Newlines are
// rejected upstream in config validation.
func gitConfigValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}
