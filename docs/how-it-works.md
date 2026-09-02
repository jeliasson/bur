# How it works

```text
HOST                                    CONTAINER (rootless podman, --rm)
┌──────────────────────────┐            ┌─────────────────────────────┐
│ nix print-dev-env        │            │ minimal base (bash, certs)  │
│ of the project devshell  │   mount    │  /nix/store       ◀── ro    │
│ /nix/store ──────────────┼───────────▶│  $PROJECT         ◀── rw    │
│ $PROJECT ────────────────┼───────────▶│  ~/.claude[.json] ◀── rw    │
│ ~/.claude ───────────────┼───────────▶│  └─ claude --skip-perms     │
└──────────────────────────┘            └─────────────────────────────┘
```

## The devshell is the environment

The devshell is built **on the host** (where nix and all caches live) and its
environment sourced inside the container; a `--profile` link under
`~/.cache/bur/gcroots/` protects it from garbage collection (`bur clean`
prunes the links of projects that no longer exist). When both
exist, `shell.nix` wins; a bare `flake.nix` uses its default devShell -
pure and pinned by `flake.lock`, so in a git repo only tracked files are
visible to the evaluation. The dev server
runs inside the same container with the same toolchain - publish its port and
you're done. Commands are resolved through the host PATH to store paths, so
`claude`, `bash`, etc. work even when the devshell doesn't include them.

The project is mounted at its **host path**, not a generic `/workspace`: the
prompt tells you which project a sandbox belongs to, and claude keys its
per-directory history on the cwd, so sessions map to the right project and
are resumable from the host too.

## Clipboard bridge

Pasting works, images included (ctrl+v in claude): agents read the
clipboard by shelling out to `wl-paste`/`xclip`, and bur puts shims by
those names on the sandbox PATH that forward reads over a unix socket to
the real tool on the host - `wl-paste` on Wayland, `xclip` on X11, whichever
matches your session must be installed there (any install works; it runs
on the host, so the nix-store rule for `cmd`/`tools` does not apply).
No compositor socket enters the container, and the bridge is strictly
read-only: the agent can paste *from* your clipboard, never write to it.
Works with `network: none` too; opt out entirely with `clipboard: false`.

## Mid-session packages: `bur-pkg`

When the agent hits a missing tool - say it wants to rasterize an svg and
the devshell has no imagemagick - it can pull one from nixpkgs mid-session:
`bur-pkg add imagemagick` inside the sandbox asks the host over a unix
socket, the host realizes `nixpkgs#imagemagick` with its own nix, and the
tools land on the sandbox PATH immediately (new store paths appear live
through the read-only `/nix/store` mount - no restart, no rebuild). No nix
machinery enters the cage: names are validated attribute paths resolved
against the **host's** nixpkgs registry, never against anything the agent
can write to, and the host never runs what it builds. Nothing is installed
either - the package is realized into the store behind a GC root that dies
with the sandbox, so the next `nix store gc` sweeps it away. Because the
host does the fetching, this works under `network: none` too. A short
`/run/bur/AGENT.md` inside the sandbox tells the agent all of this; opt
out with `nix.pkgAdd: false`.
