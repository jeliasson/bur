# Configuration

Global `~/.config/bur/config.yaml`, per-project `.bur.yaml` (found by walking
up to the nearest `.bur.yaml` or `.git`). Both files are optional. Scalars
override, lists concatenate, `env` merges. `bur init` writes a commented
starter `.bur.yaml` in the current directory, plus a gitignored `.bur.env`
for secrets; it only creates what is missing, so it is safe to re-run.

Without any config, bur behaves as if you had written:

```yaml
cmd: [claude, --dangerously-skip-permissions]
network: open                   # full egress
hostAccess: false               # no host.containers.internal
clipboard: true                 # read-only paste bridge (text & images)
# nix.shell auto-detects: shell.nix first, then the flake's default devShell
# tools, ports, mounts and env start empty
```

Everything you can set, with example (non-default) values:

```yaml
cmd:                            # what the sandbox runs instead of claude
  - opencode
tools:                          # agent companion CLIs, resolved from the host PATH
  - openspec
  - gh
  - rg
ports:                          # published on 127.0.0.1; taken host ports fall through to the next free
                                # prefix a host IP to widen: "0.0.0.0:5173:5173" exposes to the LAN
  - 8000
  - "5173:5173"
mounts:
  - "~/fixtures:/fixtures:ro"
env:
  TARS_HONESTY_LEVEL: "0.9"
envFile: .env                   # where secrets live, relative to this file
                                # (default: .bur.env; "" reads none)
network: none                   # open | none  ("filtered" reserved for v2)
hostAccess: true                # adds host.containers.internal
clipboard: false                # kill the paste bridge, e.g. for sensitive work
nix:
  shell: ./shell.nix            # a nix file, or a flake installable like ".#dev"
  pkgAdd: false                 # kill the bur-pkg bridge (default: on)
git:                            # identity for commits made inside the sandbox
  name: Johan Eliasson          # default: bur
  email: johan@example.com      # default: bur@noreply.local
  signingKey: ~/.config/bur/signing_key   # optional, enables SSH commit signing
```

`tools:` lists agent companion CLIs (spec tools, `gh`, `rg`, ...) that follow
the agent rather than the project - like `~/.claude`, they belong to every
sandbox, so putting them in each project's devshell would be the wrong
altitude. Each name is resolved on the host through symlinks to its nix
store package and that package's `bin/` is appended to the container PATH -
after the devshell, so a project-pinned version of the same tool wins.
Like the main command (and unlike the devshell profile), these store paths
are not GC-rooted.

## Git identity

The host's git config is never mounted. bur generates a read-only gitconfig
at `/run/bur/gitconfig` (wired up via `GIT_CONFIG_GLOBAL`) with the
configured identity, `init.defaultBranch main`, no credential helper, and
signing off. The default identity is a neutral `bur <bur@noreply.local>`,
so agent commits are visibly agent commits until you put your own name in
the global config; a project's `.bur.yaml` can override either field.
`bur init --git` sets this up once: it offers to copy your host git
identity into the global config and to generate the signing key below.

`signingKey:` opts in to SSH commit signing. The key is bind-mounted
read-only and must be passphrase-less - there is nobody in the sandbox to
type one. Use a dedicated key, not your auth key: generate it with
`ssh-keygen -t ed25519 -N "" -f ~/.config/bur/signing_key` and upload the
`.pub` to GitHub as a **signing** key, which cannot authenticate. Know the
trade: the agent can read the key, and with `network: open` a hostile one
could exfiltrate it and sign commits as you until you revoke it - a bounded
risk, but a real one. Signing also needs `ssh-keygen` inside the sandbox,
so keep openssh in the devshell (or `bur-pkg add openssh`).

The key lives under `~/.config/bur/`, and unlike `~/.ssh/`, that is a
directory dotfile managers and backups happily sweep up - exclude the key
there, or it may end up in a dotfiles repo. `bur init --git` keeps the
directory at 0700 as a backstop.

## Secrets

`.bur.yaml` is meant to be committed, so an API key does not belong in its
`env:` block - and gitignoring the file itself would take the whole project
config with it. Keep secrets in `.bur.env` next to it instead. `bur init`
scaffolds and gitignores it alongside the config (and creates just the
missing one in a fresh clone); a project without secrets can simply delete
it - a missing `.bur.env` is fine.

The format is `KEY=VALUE` per line, `#` for comments, one optional layer of
quotes stripped, no interpolation and no multiline values. bur loads it from
the project root on top of `env:`, so a key set in both wins here. Values
reach the sandbox through a mode-0600 file podman reads, never as command
line arguments - the `podman run` process lives as long as the sandbox and
its `/proc` entry is world-readable.

`.bur.env` is only the default name. A project that already keeps its
secrets somewhere points `envFile:` at it, and `bur init` then scaffolds
and gitignores that file instead:

```yaml
envFile: .env       # relative to .bur.yaml; ~ and absolute paths work too,
                    # for one secrets file shared across projects.
                    # "" reads no env file at all.
```

A `~` or absolute path (or `""`) makes `bur init` skip the secrets file
entirely - a file shared across projects is yours to create and protect.

A configured file that does not exist is a warning on startup, while a
missing `.bur.env` is silent - not having one is the normal case.
