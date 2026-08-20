// Package config loads and merges bur configuration from the global
// config file and the project's .bur.yaml, and locates the project root.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Cmd        []string
	Tools      []string
	Mounts     []string
	Ports      []string
	Env        map[string]string
	Network    string
	HostAccess bool
	Clipboard  bool
	NixShell   string
}

func Default() Config {
	return Config{
		Cmd:       []string{"claude", "--dangerously-skip-permissions"},
		Env:       map[string]string{},
		Network:   "open",
		Clipboard: true,
	}
}

// flexString accepts both `8000` and `"8000:80"` in YAML lists.
type flexString string

func (s *flexString) UnmarshalYAML(n *yaml.Node) error {
	*s = flexString(n.Value)
	return nil
}

type fileConfig struct {
	Cmd        []string          `yaml:"cmd"`
	Tools      []string          `yaml:"tools"`
	Mounts     []string          `yaml:"mounts"`
	Ports      []flexString      `yaml:"ports"`
	Env        map[string]string `yaml:"env"`
	Network    *string           `yaml:"network"`
	HostAccess *bool             `yaml:"hostAccess"`
	Clipboard  *bool             `yaml:"clipboard"`
	Nix        *struct {
		Shell *string `yaml:"shell"`
	} `yaml:"nix"`
}

var knownTopKeys = map[string]bool{
	"cmd": true, "tools": true, "mounts": true, "ports": true, "env": true,
	"network": true, "hostAccess": true, "clipboard": true, "nix": true,
}

var knownNixKeys = map[string]bool{"shell": true}

func loadFile(path string) (*fileConfig, []string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	var warnings []string
	for k, v := range raw {
		if !knownTopKeys[k] {
			warnings = append(warnings, fmt.Sprintf("%s: unknown key %q", path, k))
			continue
		}
		if k == "nix" {
			if nested, ok := v.(map[string]any); ok {
				for nk := range nested {
					if !knownNixKeys[nk] {
						warnings = append(warnings, fmt.Sprintf("%s: unknown key %q", path, "nix."+nk))
					}
				}
			}
		}
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, warnings, fmt.Errorf("%s: %w", path, err)
	}
	return &fc, warnings, nil
}

// apply merges a file config into cfg: scalars override, lists concat, env maps merge.
func (cfg *Config) apply(fc *fileConfig) {
	if fc == nil {
		return
	}
	if len(fc.Cmd) > 0 {
		cfg.Cmd = fc.Cmd
	}
	cfg.Tools = append(cfg.Tools, fc.Tools...)
	cfg.Mounts = append(cfg.Mounts, fc.Mounts...)
	for _, p := range fc.Ports {
		cfg.Ports = append(cfg.Ports, string(p))
	}
	for k, v := range fc.Env {
		cfg.Env[k] = v
	}
	if fc.Network != nil {
		cfg.Network = *fc.Network
	}
	if fc.HostAccess != nil {
		cfg.HostAccess = *fc.HostAccess
	}
	if fc.Clipboard != nil {
		cfg.Clipboard = *fc.Clipboard
	}
	if fc.Nix != nil && fc.Nix.Shell != nil {
		cfg.NixShell = *fc.Nix.Shell
	}
}

func (cfg *Config) validate() error {
	switch cfg.Network {
	case "open", "none":
	case "filtered":
		return fmt.Errorf("network: filtered egress is not yet supported (use \"open\" or \"none\")")
	default:
		return fmt.Errorf("network: unknown value %q (use \"open\" or \"none\")", cfg.Network)
	}
	return nil
}

// GuardRoot refuses a project root whose mount would cover the user's
// home directory - the walk lands there when ~ itself has a .git (a
// dotfiles checkout) or when no marker exists anywhere. Silently mounting
// all of home would hand ~/.ssh and every other repo to the agent. A
// .bur.yaml at the root is the explicit opt-in.
func GuardRoot(root string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil // no home to protect; mounts fail later if this matters
	}
	return guardRoot(root, home)
}

func guardRoot(root, home string) error {
	if !pathCovers(root, home) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".bur.yaml")); err == nil {
		return nil
	}
	return fmt.Errorf("project root %s covers your home directory - mounting it would put ~/.ssh and every other repo inside the cage (run from a project with its own .git or .bur.yaml, or create %s to opt in)",
		root, filepath.Join(root, ".bur.yaml"))
}

// pathCovers reports whether mounting dir would include sub.
func pathCovers(dir, sub string) bool {
	if dir == sub || dir == "/" {
		return true
	}
	return strings.HasPrefix(sub, dir+"/")
}

// FindProjectRoot walks up from cwd to the first directory containing
// .bur.yaml, falling back to the first containing .git, else cwd itself.
func FindProjectRoot(cwd string) string {
	gitRoot := ""
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".bur.yaml")); err == nil {
			return dir
		}
		if gitRoot == "" {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				gitRoot = dir
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if gitRoot != "" {
		return gitRoot
	}
	return cwd
}

func Load(cwd string) (Config, string, []string, error) {
	root := FindProjectRoot(cwd)
	cfg := Default()
	var warnings []string

	if confDir, err := os.UserConfigDir(); err == nil {
		fc, w, err := loadFile(filepath.Join(confDir, "bur", "config.yaml"))
		if err != nil {
			return cfg, root, warnings, err
		}
		warnings = append(warnings, w...)
		cfg.apply(fc)
	}

	fc, w, err := loadFile(filepath.Join(root, ".bur.yaml"))
	if err != nil {
		return cfg, root, warnings, err
	}
	warnings = append(warnings, w...)
	cfg.apply(fc)

	if err := cfg.validate(); err != nil {
		return cfg, root, warnings, err
	}
	return cfg, root, warnings, nil
}
