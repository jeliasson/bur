# Usage

| Command        | What it does                                                    |
| -------------- | --------------------------------------------------------------- |
| `bur`          | start a sandbox, running: `claude --dangerously-skip-permissions` |
| `bur bash`     | same sandbox environment, but a shell instead                   |
| `bur opencode` | or any other agent/command                                      |
| `bur exec bash`| second process inside a running sandbox of this project         |
| `bur ls`       | list running sandboxes                                          |
| `bur clean`    | remove every bur container and stale devshell GC roots (asks first; `-f` skips) |
| `bur init`     | write a starter `.bur.yaml` and a gitignored `.bur.env` for secrets - only what is missing |

## Sandbox lifecycle

Sandboxes are one-shot: every `bur` starts a fresh container (named
`bur-<project>-<adjective>-<animal>`) that lives exactly as long as its main
process. Exit claude and it is gone - no daemon, no stack, no state directory
in the project, and as many parallel sandboxes per project as you like.
Closing the terminal hangs up the agent, which ends the sandbox too; should
one ever be orphaned (terminal SIGKILLed, agent ignoring SIGHUP), `bur clean`
sweeps it away. Session persistence is a job for your terminal multiplexer,
not the cage.

## `bur exec`

`bur exec` finds the sandboxes of the current project (by the same root walk
as the config). With several running it shows a picker - newest first, the
one started from your directory as the default - and `bur exec --in <name>`
(full name, or a unique suffix like `witty-yak`) skips the prompt, for
scripts or piped stdin.

## Agent state

Only claude's state (`~/.claude`, `~/.claude.json`) follows the agent into
the sandbox. Other agents start from scratch - add their state dirs to
`mounts:` (e.g. `~/.local/share/opencode`) to keep logins and history. The
same goes for `~/.gitconfig`: commits made inside the sandbox carry no
author identity unless you mount it (or use the built-in
[git identity](configuration.md#git-identity)).
