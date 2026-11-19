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

    # nixpkgs lags templ; keep this in step with the templ version in go.mod
    # (the *_templ.go files and the drift check use it). Drop when nixpkgs catches up.
    templOverlay = final: prev: {
      templ =
        if prev.lib.versionAtLeast prev.templ.version "0.3.1118" then
          throw "nixpkgs now ships templ ${prev.templ.version}: delete templOverlay from flake.nix"
        else
          prev.templ.overrideAttrs (old: rec {
            version = "0.3.1118";
            src = prev.fetchFromGitHub {
              owner = "a-h";
              repo = "templ";
              rev = "v${version}";
              hash = "sha256-4ARBfwLD37FSCC3pgYWomsXRThl8NkHLte8awD0/Ddo=";
            };
            vendorHash = "sha256-SI3Y7eqw+sLvBmRi7r4Oc30ovIKvFRGxdXF8fyKog9g=";
          });
    };

    perSystem = forEachSystem (system: let
      pkgs = import nixpkgs { inherit system; overlays = [ templOverlay ]; };

      version = "2.0.2";
      commitOrDirty = if self ? rev then self.rev else "dirty";

      breezyd-pkg = pkgs.buildGoModule {
        pname = "breezyd";
        inherit version;
        src = ./.;
        vendorHash = "sha256-o9F35I8Zz2lT6dPDnTA2BRPcWL9hHU9giYm4YDsf5Ug=";
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

    moduleSystem = extra: nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        self.nixosModules.default
        {
          boot.isContainer = true;
          system.stateVersion = "26.05";
          networking.hostName = "my-host";
          services.breezyd = {
            enable = true;
            settings.daemon.listen = "127.0.0.1:9876";
            prometheus.enable = true;
            push.offsite.url = "http://offsite:8428";
          };
        }
        extra
      ];
    };
    # Evaluation fails (so the check does) if the options stop doing their job.
    moduleCheck =
      let
        vm = (moduleSystem { services.victoriametrics.enable = true; }).config;
        prom = (moduleSystem {
          services.prometheus.enable = true;
          services.prometheus.extraFlags = [ "--web.enable-remote-write-receiver" ];
        }).config;
        lib = nixpkgs.lib;
        job = jobs: lib.findFirst (j: j.job_name == "breezyd") null jobs;
        drops = j: lib.any (r: r.source_labels == [ "pushed_my_host" ] && r.regex == "true" && r.action == "drop") j.metric_relabel_configs;
      in
      assert (vm.services.breezyd.settings.daemon.push).my_host == "http://127.0.0.1:8428";
      assert (vm.services.breezyd.settings.daemon.push).offsite == "http://offsite:8428";
      assert drops (job vm.services.victoriametrics.prometheusConfig.scrape_configs);
      assert vm.services.prometheus.scrapeConfigs == [ ];
      assert (prom.services.breezyd.settings.daemon.push).my_host == "http://127.0.0.1:9090";
      assert drops (job prom.services.prometheus.scrapeConfigs);
      vm.systemd.units."breezyd.service".unit;
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
    checks     = forEachSystem (system: perSystem.${system}.checks // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") {
      module = moduleCheck;
    });
    devShells  = forEachSystem (system: perSystem.${system}.devShells);
    formatter  = forEachSystem (system: perSystem.${system}.formatter);
  };
}
