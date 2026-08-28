{
  description = "Run AI coding agents in rootless podman sandboxes with nix devshell environments";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      overlays.default = final: _prev: {
        bur = final.callPackage ./package.nix { };
      };

      packages = forAllSystems (pkgs: rec {
        bur = pkgs.callPackage ./package.nix { };
        default = bur;
      });

      # buildGoModule runs go test in its checkPhase, so the package is the check.
      checks = self.packages;

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls ];

          # Source builds embed no base image; `make base-image` loads the
          # tar this tag belongs to.
          BUR_BASE_IMAGE = "bur-base:${self.packages.${pkgs.stdenv.hostPlatform.system}.bur.baseImage.imageTag}";

          shellHook = ''
            if command -v podman >/dev/null && ! podman image exists "$BUR_BASE_IMAGE"; then
              echo "bur: run 'make base-image' to load $BUR_BASE_IMAGE"
            fi
          '';
        };
      });

      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.programs.bur;
        in
        {
          options.programs.bur = {
            enable = lib.mkEnableOption "bur, AI agent sandboxes in rootless podman";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
              defaultText = lib.literalExpression "bur.packages.\${system}.default";
              description = "The bur package to install.";
            };
          };

          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];
            virtualisation.podman.enable = true;
          };
        };
    };
}
