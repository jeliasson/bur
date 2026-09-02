# Security model

What the cage does and doesn't do.

## Protected ✅

- The rest of your home (`~/.ssh`, other repos). bur refuses to start
  when the project root would resolve to `$HOME` itself - the classic
  trap is a dotfiles `.git` in `~` - so the home mount can never happen
  by accident; a `.bur.yaml` placed there is the explicit opt-in.
- Your nix store (mounted read-only), your OS.
- Workspace file ownership - the agent runs as your uid via
  `--userns=keep-id`, so workspace files stay yours.
- Published `ports:` bind to `127.0.0.1` by default, so a service the agent
  starts is reachable from the host but not the LAN. Opt into wider exposure
  per-port with an explicit host IP (`"0.0.0.0:5173:5173"`).
- Your display server. Clipboard paste goes through a read-only bridge
  (text and image types only); no Wayland or X11 socket is ever mounted,
  and clipboard *writes* are not bridged - an agent that could seed your
  clipboard could poison your next paste into a terminal.

## Not protected ⚠️

- The project mount is your live checkout, fully writable - including
  `.git`, so local history can go down with the ship. The remote is the
  real safety net, and since neither `~/.ssh` nor `~/.git-credentials` is
  mounted, the agent normally has no way to push to it either.
- A cloned repo is trusted input before any cage exists: its `.bur.yaml`
  can add mounts, publish ports, set env, and replace the command, and its
  devshell (`shell.nix` / flake) is evaluated and built on the host when
  `bur` starts. Review both before the first run in an untrusted checkout.
- `~/.claude` is mounted rw - the agent can edit its own global settings.
- Secrets you pass via `env` or `.bur.env` + open egress = an exfiltration
  channel. Keeping them out of git does not keep them out of the cage. Use
  `network: none` for sensitive work; a deny-by-default egress allowlist
  (`network: filtered`) is the planned v2 flagship.
- Host services bound to `127.0.0.1` are unreachable even with `hostAccess`
  (pasta maps `host.containers.internal` to the host's external address) -
  bind `0.0.0.0` or, better, run the service in the sandbox.
- The host clipboard is readable by the agent the whole time a sandbox
  runs - that is what makes paste work. Copy a password from your manager
  mid-session and the agent could read it; set `clipboard: false` for
  sensitive work.
- `bur-pkg` lets the agent fetch any nixpkgs package via the host - trusted
  code from your own nixpkgs pin, never evaluated from agent-writable files,
  and gone after the next GC, but still your disk and bandwidth. Set
  `nix.pkgAdd: false` to close that valve.

Changing the *environment* mid-session is still deliberate friction: the
agent edits `shell.nix`, you restart `bur` - devshell changes get reviewed
like code. `bur-pkg` is the sanctioned exception for grabbing a one-off
tool, which is why it is limited to bare attribute names from the host's
own nixpkgs and leaves no trace past the sandbox.
