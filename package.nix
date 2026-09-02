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
  # The image's /bin symlinks resolve against the host store, which is
  # mounted over the image's own - so this closure has to survive gc there.
  baseRoot = buildEnv {
    name = "bur-base-root";
    paths = [ bashInteractive coreutils cacert ];
    pathsToLink = [ "/bin" "/etc" ];
  };

  base = dockerTools.buildImage {
    name = "bur-base";

    copyToRoot = baseRoot;

    # Real files, not dockerTools.fakeNss: its store symlinks dangle once
    # the host gcs them, and a broken /etc/passwd both fails every
    # getpwuid() and stops podman's --userns=keep-id writing the real user.
    extraCommands = ''
      mkdir -p tmp home/bur run/bur var/empty
      chmod 1777 tmp

      # buildEnv links a whole directory when only cacert provides it.
      if [ -L etc ]; then
        etcTarget=$(readlink -f etc)
        rm etc && mkdir etc && cp -a "$etcTarget"/. etc/
      fi
      mkdir -p etc
      chmod -R u+w etc
      rm -f etc/passwd etc/group etc/nsswitch.conf

      cat > etc/passwd <<'EOF'
      root:x:0:0:System administrator:/root:/bin/sh
      nobody:x:65534:65534:Unprivileged account:/var/empty:/bin/sh
      EOF

      cat > etc/group <<'EOF'
      root:x:0:
      nobody:x:65534:
      EOF

      echo 'hosts: files dns' > etc/nsswitch.conf
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
  version = "0.3.1";

  src = lib.cleanSource ./.;
  vendorHash = "sha256-g+yaVIx4jxpAQ/+WrGKxhVeliYx7nLQe/zsGpxV4Fn4=";

  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.baseImageTar=${base}"
    "-X main.baseImageRef=bur-base:${base.imageTag}"
    # Embedding the path is what makes it a runtime dep, so a gc root on
    # bur keeps it alive; the image tar is compressed and scans as none.
    "-X main.baseImageRoot=${baseRoot}"
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
