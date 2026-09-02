package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const signingKeyFile = "signing_key"

// InitGit sets up the global git identity for sandboxes: copies the host's
// git user.name/user.email into ~/.config/bur/config.yaml and optionally
// generates a dedicated SSH signing key. One-time convenience - after this,
// the host git config is never consulted again.
func InitGit(force bool) error {
	confDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	return initGit(filepath.Join(confDir, "bur"), asker(force), hostGitIdentity, sshKeygen)
}

func initGit(burDir string, ask func(string) bool, ident func() (string, string), keygen func(string) error) error {
	// keygen writes here before appendToFile would create the dir, and 0700
	// keeps a passphrase-less key out of other users' reach.
	if err := os.MkdirAll(burDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(burDir, "config.yaml")
	fc, _, err := loadFile(path)
	if err != nil {
		return err
	}
	if fc != nil && fc.Git != nil {
		fmt.Printf("- %s already has a git: block, left alone\n", path)
		return nil
	}

	name, email := Default().GitName, Default().GitEmail
	if hn, he := ident(); hn != "" && he != "" {
		if ask(fmt.Sprintf("Use your host git identity %q <%s> for sandbox commits?", hn, he)) {
			name, email = hn, he
		}
	} else {
		fmt.Println("- No git identity found on the host, using the neutral default")
	}

	keyPath := filepath.Join(burDir, signingKeyFile)
	signing := false
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Println("- Signing key", keyPath, "exists, left alone")
		signing = true
	} else if ask("Generate a dedicated SSH signing key so sandbox commits show as verified?") {
		if err := keygen(keyPath); err != nil {
			return fmt.Errorf("generating signing key: %w", err)
		}
		signing = true
		fmt.Println("- Wrote", keyPath, "- upload the .pub to GitHub as a *signing* key:")
		fmt.Println("    gh ssh-key add --type signing --title \"bur signing key\"", keyPath+".pub")
		fmt.Println("  or paste it at https://github.com/settings/ssh/new (key type: Signing Key)")
	}

	if signing {
		// Tighten a pre-existing 0755 dir once a key lives in it.
		if err := os.Chmod(burDir, 0o700); err != nil {
			return err
		}
	}

	block := gitBlock(name, email, signing)
	if err := appendToFile(path, block); err != nil {
		return err
	}
	fmt.Printf("- Added a git: block to %s\n", path)
	return nil
}

// gitBlock renders the YAML to append; strconv.Quote is close enough to
// YAML double-quoting for names and emails.
func gitBlock(name, email string, signing bool) string {
	b := fmt.Sprintf("git:\n  name: %s\n  email: %s\n", strconv.Quote(name), strconv.Quote(email))
	if signing {
		b += "  signingKey: ~/.config/bur/" + signingKeyFile + "\n"
	}
	return b
}

func appendToFile(path, block string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out+block), 0o644)
}

func hostGitIdentity() (string, string) {
	get := func(key string) string {
		out, err := exec.Command("git", "config", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

func sshKeygen(path string) error {
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "bur signing key", "-q", "-f", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
