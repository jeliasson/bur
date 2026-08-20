package sandbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Sandbox is a running bur container, reconstructed from podman labels -
// bur keeps no state of its own.
type Sandbox struct {
	Name    string
	Root    string
	Cwd     string
	Agent   string
	Started time.Time
}

func List() ([]Sandbox, error) {
	out, err := exec.Command("podman", "ps",
		"--filter", "label=bur.root", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("podman ps: %w", err)
	}
	var rows []struct {
		Names     []string          `json:"Names"`
		Labels    map[string]string `json:"Labels"`
		StartedAt int64             `json:"StartedAt"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing podman ps output: %w", err)
	}
	var sbs []Sandbox
	for _, r := range rows {
		if len(r.Names) == 0 {
			continue
		}
		sbs = append(sbs, Sandbox{
			Name:    r.Names[0],
			Root:    r.Labels["bur.root"],
			Cwd:     r.Labels["bur.cwd"],
			Agent:   r.Labels["bur.agent"],
			Started: time.Unix(r.StartedAt, 0),
		})
	}
	return sbs, nil
}

// Resolve picks the sandbox an exec targets: the ones started for
// this project root, with an interactive pick when several are running.
func Resolve(root, cwd string) (Sandbox, error) {
	all, err := List()
	if err != nil {
		return Sandbox{}, err
	}
	var cands []Sandbox
	for _, s := range all {
		if s.Root == root {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return Sandbox{}, fmt.Errorf("no sandbox is running for %s (start one with `bur`)", root)
	}
	orderCandidates(cands, cwd)
	if len(cands) == 1 {
		return cands[0], nil
	}
	return pickSandbox(cands)
}

// orderCandidates sorts newest-first, with sandboxes started from exactly
// cwd on top - the picker default should be the one "here".
func orderCandidates(cands []Sandbox, cwd string) {
	sort.SliceStable(cands, func(i, j int) bool {
		if (cands[i].Cwd == cwd) != (cands[j].Cwd == cwd) {
			return cands[i].Cwd == cwd
		}
		return cands[i].Started.After(cands[j].Started)
	})
}

// pickSandbox prompts on /dev/tty rather than stdin/stdout, which belong
// to the command being exec'd (`echo hi | bur exec cat` must still work).
func pickSandbox(cands []Sandbox) (Sandbox, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		names := make([]string, len(cands))
		for i, s := range cands {
			names[i] = s.Name
		}
		return Sandbox{}, fmt.Errorf("%d sandboxes are running for this project: %s (pick one with `bur exec --in <name>`)",
			len(cands), strings.Join(names, ", "))
	}
	defer tty.Close()

	fmt.Fprintf(tty, "bur: %d sandboxes running:\n", len(cands))
	w := tabwriter.NewWriter(tty, 2, 0, 3, ' ', 0)
	for i, s := range cands {
		fmt.Fprintf(w, "  %d.\t%s\t%s\tup %s\t%s\n",
			i+1, s.Name, s.Agent, fmtAge(s.Started), AbbrevHome(s.Cwd))
	}
	w.Flush()
	fmt.Fprintf(tty, "run in which? [1] ")
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return Sandbox{}, fmt.Errorf("reading choice: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return cands[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(cands) {
		return Sandbox{}, fmt.Errorf("invalid choice %q", line)
	}
	return cands[n-1], nil
}

// MatchByName resolves an --in argument: exact container name, or a unique
// suffix of one (`--in brave-otter`, or just `--in otter`, works without
// the bur-<project>- prefix).
func MatchByName(all []Sandbox, arg string) (string, error) {
	var suffix []string
	for _, s := range all {
		if s.Name == arg {
			return arg, nil
		}
		if strings.HasSuffix(s.Name, "-"+arg) {
			suffix = append(suffix, s.Name)
		}
	}
	switch len(suffix) {
	case 1:
		return suffix[0], nil
	case 0:
		return "", fmt.Errorf("no running sandbox named %q (see `bur ls`)", arg)
	default:
		return "", fmt.Errorf("%q is ambiguous: %s", arg, strings.Join(suffix, ", "))
	}
}

// Ls prints the `bur ls` table of running sandboxes.
func Ls() error {
	sbs, err := List()
	if err != nil {
		return err
	}
	if len(sbs) == 0 {
		fmt.Println("no sandboxes running")
		return nil
	}
	sort.Slice(sbs, func(i, j int) bool { return sbs[i].Started.After(sbs[j].Started) })
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tAGENT\tUP\tDIR")
	for _, s := range sbs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Agent, fmtAge(s.Started), AbbrevHome(s.Cwd))
	}
	return w.Flush()
}

// Clean force-removes every bur container, running or not - the recovery
// hatch for sandboxes orphaned by a dead terminal. Filtering on bur.agent
// (set since the first release) also catches containers from older burs.
// Returns how many containers were removed; the caller owns the
// "nothing to clean" messaging (it also prunes devshell gc roots).
func Clean(force bool) (int, error) {
	out, err := exec.Command("podman", "ps", "-a",
		"--filter", "label=bur.agent", "--format", "{{.Names}}").Output()
	if err != nil {
		return 0, fmt.Errorf("podman ps: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return 0, nil
	}
	if !force {
		prompt := fmt.Sprintf("remove %d container(s): %s?", len(names), strings.Join(names, ", "))
		if !confirm(prompt) {
			return 0, fmt.Errorf("aborted (`bur clean -f` skips the prompt)")
		}
	}
	if err := RunPodman(append([]string{"rm", "-f"}, names...), ""); err != nil {
		return 0, err
	}
	return len(names), nil
}

func confirm(prompt string) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// AbbrevHome shortens a path with the ~ convention for display.
func AbbrevHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}
