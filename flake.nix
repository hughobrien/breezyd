{
  description = "breezyd — Go library, daemon, and CLI for Vents Twinfresh Breezy / Elite 160 Pro HRV units";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }: let
    moduleOutputs = {
      nixosModules.default = ./nix/module.nix;
      nixosModules.breezyd = ./nix/module.nix;
    };
  in moduleOutputs // flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        version = "1.5.0";
        commitOrDirty = if self ? rev then self.rev else "dirty";

        breezyd-pkg = pkgs.buildGoModule {
          pname = "breezyd";
          inherit version;
          src = ./.;
          vendorHash = "sha256-TQW/KUuf9pI7UmkkvkzZcPWwEJDMraHbR582Q4725Vo=";
          subPackages = [ "cmd/breezyd" "cmd/breezy" ];
          # Reproducible: omit `-X main.date=…` so two builds of the same
          # commit produce identical binaries.
          ldflags = [
            "-s" "-w"
            "-X main.version=${version}"
            "-X main.commit=${commitOrDirty}"
          ];
          # This is a network-protocol package — its tests don't need
          # network access, but the integration tests need a real device
          # behind a build tag. Run only the default test set.
          doCheck = true;
          meta = with pkgs.lib; {
            description = "Go library, daemon, and CLI for Vents Twinfresh Breezy ERVs";
            homepage = "https://github.com/hughobrien/breezyd";
            license = licenses.gpl3Plus;
            platforms = platforms.unix;
            mainProgram = "breezyd";
          };
        };
      in {
        packages = {
          default = breezyd-pkg;
          breezyd = breezyd-pkg;
          breezy = breezyd-pkg; # same derivation produces both binaries
        };

        apps = {
          default = {
            type = "app";
            program = "${breezyd-pkg}/bin/breezyd";
          };
          breezyd = {
            type = "app";
            program = "${breezyd-pkg}/bin/breezyd";
          };
          breezy = {
            type = "app";
            program = "${breezyd-pkg}/bin/breezy";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            go-tools
            goreleaser
            just
          ];
        };

        formatter = pkgs.nixpkgs-fmt;
      });
}
