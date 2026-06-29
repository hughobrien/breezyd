{
  description = "breezyd — Go library, daemon, and CLI for Vents Twinfresh Breezy / Elite 160 Pro HRV units";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    # Inlined replacement for flake-utils.lib.eachDefaultSystem. Builds the
    # per-system attrset once (so pkgs / breezyd-pkg evaluate once per
    # system), then transposes via genAttrs so the flake exports the
    # standard `packages.<system>.*`, `apps.<system>.*`, etc. shape.
    systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
    forEachSystem = f: nixpkgs.lib.genAttrs systems f;

    perSystem = forEachSystem (system: let
      pkgs = import nixpkgs { inherit system; };

      version = "2.0.2";
      commitOrDirty = if self ? rev then self.rev else "dirty";

      breezyd-pkg = pkgs.buildGoModule {
        pname = "breezyd";
        inherit version;
        src = ./.;
        vendorHash = "sha256-GlETv7Dza4XC56Ll/vJmsn4ChZIlVffRSBJl5ebS7fc=";
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
          templ
        ];
      };

      # CI merge-gate, run hermetically in the nix build sandbox.
      # nix build .#checks.<system>.gate
      checks.gate = breezyd-pkg.overrideAttrs (old: {
        pname = "breezyd-ci-gate";
        nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [ pkgs.clang pkgs.golangci-lint pkgs.templ ];
        doCheck = true;
        checkPhase = ''
          runHook preCheck
          export HOME="$TMPDIR"
          go vet ./...
          drift=$(gofmt -l . | grep -v '^vendor/' || true)
          [ -z "$drift" ] || { echo "gofmt drift: $drift"; exit 1; }
          go test ./...
          CGO_ENABLED=1 CC=clang go test -race ./...
          golangci-lint run ./...
          go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin
          cp -a cmd/breezyd/ui/templates "$TMPDIR/templ-before"
          templ generate
          diff -rq "$TMPDIR/templ-before" cmd/breezyd/ui/templates || { echo "templ drift; run 'just generate'"; exit 1; }
          runHook postCheck
        '';
      });

      formatter = pkgs.nixpkgs-fmt;
    });

    # Wrap the bare module so it defaults services.breezyd.package to the
    # flake's own build for the host's system. Without this wrapper, the
    # module falls back to pkgs.breezyd — which doesn't exist in nixpkgs
    # (this is a third-party flake) — and throws on evaluation.
    defaultModule = { pkgs, lib, ... }: {
      imports = [ ./nix/module.nix ];
      services.breezyd.package = lib.mkDefault
        self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
  in {
    nixosModules.default = defaultModule;
    nixosModules.breezyd = defaultModule;

    packages   = forEachSystem (system: perSystem.${system}.packages);
    apps       = forEachSystem (system: perSystem.${system}.apps);
    checks     = forEachSystem (system: perSystem.${system}.checks);
    devShells  = forEachSystem (system: perSystem.${system}.devShells);
    formatter  = forEachSystem (system: perSystem.${system}.formatter);
  };
}
