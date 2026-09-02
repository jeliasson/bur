# Installation

Requirements: linux, [rootless podman](https://podman.io/docs/installation),
and nix on the host - building the project devshell is the whole point.

## Nix profile

```sh
nix profile install github:jeliasson/bur
```

## First-run notes

Two things to know for the first run: the command bur starts (`claude` by
default) must be nix-installed - on the host or in the devshell - so it
resolves to `/nix/store`; a `claude` from npm will not exist inside the
sandbox. And a project without a devshell still works - you get a minimal
sandbox (bash, CA certificates, your command), just no toolchain.

## NixOS

The module installs bur and takes care of rootless podman:

```nix
{
  imports = [ inputs.bur.nixosModules.default ];
  programs.bur.enable = true;
}
```

bur is also consumable as a flake input via `overlays.default` or
`packages.<system>.default`.

## Other Linux distros

Install rootless podman from your distro's packages and
[nix](https://nixos.org/download/) from any installer; the quick start then
works as-is. One thing to know: `nix profile install` needs flakes enabled
(`experimental-features = nix-command flakes` in `~/.config/nix/nix.conf`).

## Build from source

The binary is plain Go, so nix is not needed to build it:

```sh
git clone https://github.com/jeliasson/bur && cd bur
make build   # or: go build ./cmd/bur
```

A source build does not embed the `bur-base` image; point `BUR_BASE_IMAGE`
at any image that provides bash and CA certificates. `nix develop` sets it
to the flake's own image, which `make base-image` loads. Note that bur still
needs nix on the host at runtime - the project devshell is the container
environment, and commands are resolved through `/nix/store`. Making bur
useful without nix would be a new environment backend (see the roadmap in
the README), not a build flavor.
