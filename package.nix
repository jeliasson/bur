# bur - run AI agents (claude by default) in a rootless podman sandbox.
# The project devshell is built on the host and /nix/store is mounted
# read-only, so no per-project images exist. The minimal base image below
# is built once per bur version and auto-loaded into podman by the CLI.

{ lib
, buildGoModule
, dockerTools
, buildEnv
, bashInteractive
, coreutils
, cacert
}:

let
  base = dockerTools.buildImage {
    name = "bur-base";

    copyToRoot = buildEnv {
      name = "bur-base-root";
      paths = [ bashInteractive coreutils cacert dockerTools.fakeNss ];
      pathsToLink = [ "/bin" "/etc" "/var" ];
    };

    extraCommands = ''
      mkdir -p tmp home/bur run/bur
      chmod 1777 tmp
    '';

    config = {
      Env = [
        "PATH=/bin"
        "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
        "NIX_SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
        "LANG=C.UTF-8"
      ];
      Cmd = [ "/bin/bash" ];
    };
  };

in
buildGoModule rec {
  pname = "bur";
  version = "0.1.0";

  src = lib.cleanSource ./.;
  vendorHash = "sha256-g+yaVIx4jxpAQ/+WrGKxhVeliYx7nLQe/zsGpxV4Fn4=";

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.baseImageTar=${base}"
    "-X main.baseImageRef=bur-base:${base.imageTag}"
  ];

  passthru.baseImage = base;

  meta = {
    description = "Run AI coding agents in rootless podman sandboxes with nix devshell environments";
    homepage = "https://github.com/jeliasson/bur";
    license = lib.licenses.mit;
    mainProgram = "bur";
    platforms = lib.platforms.linux;
  };
}
