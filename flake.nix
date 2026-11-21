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

      version = nixpkgs.lib.fileContents ./VERSION; # single source: see `just check-version`
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
        # The daemon tests resolve a state dir from HOME/XDG_STATE_HOME; the
        # nix sandbox sets HOME=/homeless-shelter (unwritable).
        preCheck = ''
          export HOME="$TMPDIR"
        '';
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
          CGO_ENABLED=1 CC=clang go test -asan ./...
          CGO_ENABLED=1 CC=clang go test -msan ./...
          golangci-lint run ./...
          go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin
          cp -a cmd/breezyd/ui/templates "$TMPDIR/templ-before"
          templ generate
          diff -rq "$TMPDIR/templ-before" cmd/breezyd/ui/templates || { echo "templ drift; run 'just generate'"; exit 1; }
          runHook postCheck
        '';
      });

      # Playwright dashboard suite (tests/ui), hermetic in the nix sandbox.
      # nix build .#checks.<system>.ui
      #
      # VERSION PIN: tests/ui/package.json pins @playwright/test to EXACTLY the
      # version of nixpkgs' playwright-driver (the browsers below), because
      # Playwright refuses a browser build it was not released with. nixpkgs
      # (flake.lock) pins playwright-driver; package.json + pnpm-lock.yaml pin
      # the npm side. The assert below fails evaluation when they drift:
      # bump package.json to the new version, regenerate pnpm-lock.yaml and
      # run `just nix-pnpm-hash`.
      checks.ui = let
        uiPkg = builtins.fromJSON (builtins.readFile ./tests/ui/package.json);
        pwVersion = pkgs.playwright-driver.version;
        # Test-admin build of the daemon (adds the /test/... mutation surface).
        breezyd-test = breezyd-pkg.overrideAttrs (_: {
          pname = "breezyd-test-admin";
          tags = [ "breezyd_test_admin" ];
          subPackages = [ "cmd/breezyd" ];
          doCheck = false;
        });
        uiSrc = ./tests/ui;
      in
      assert pkgs.lib.assertMsg (uiPkg.devDependencies."@playwright/test" == pwVersion)
        "tests/ui/package.json pins @playwright/test ${uiPkg.devDependencies."@playwright/test"} but nixpkgs playwright-driver is ${pwVersion}; pin them to the same version (see flake.nix checks.ui)";
      pkgs.stdenv.mkDerivation {
        pname = "breezyd-ui-tests";
        version = "0";
        src = uiSrc;
        nativeBuildInputs = [ pkgs.nodejs pkgs.pnpm_10 pkgs.pnpmConfigHook ];
        # Fixed-output fetch of the pnpm store from pnpm-lock.yaml. The hash
        # changes whenever tests/ui/pnpm-lock.yaml does: `just nix-pnpm-hash`.
        pnpmDeps = pkgs.fetchPnpmDeps {
          pname = "breezyd-ui-tests";
          version = "0";
          src = uiSrc;
          pnpm = pkgs.pnpm_10;
          fetcherVersion = 3;
          hash = "sha256-/OcZoeid9aVFxeoYgUynHKh8NC0yxBB5tDZ4IFQAE1g=";
        };
        env = {
          PLAYWRIGHT_BROWSERS_PATH = "${pkgs.playwright-driver.browsers}";
          PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
          # Without a fontconfig + a font, Chromium lays text out at zero size
          # and the sandbox sees every heading/summary as "hidden".
          FONTCONFIG_FILE = pkgs.makeFontsConf { fontDirectories = [ pkgs.dejavu_fonts ]; };
          BREEZYD_BIN = "${breezyd-test}/bin/breezyd";
          BREEZYD_SEED = "${./pkg/breezy/fakedevice/snapshot_148.json}";
          CI = "1";
        };
        dontBuild = true;
        doCheck = true;
        checkPhase = ''
          runHook preCheck
          export HOME="$TMPDIR"
          pnpm exec playwright test
          runHook postCheck
        '';
        installPhase = "touch $out";
      };

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
        ng = (moduleSystem {
          services.breezyd.nginx.enable = true;
          services.breezyd.nginx.virtualHost = "breezy.home.lan";
        }).config;
        ngLoc = ng.services.nginx.virtualHosts."breezy.home.lan".locations."/";
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
      assert ngLoc.proxyPass == "http://127.0.0.1:9876";
      assert lib.hasInfix "proxy_set_header X-Forwarded-Host $http_host;" ngLoc.extraConfig;
      assert lib.hasInfix "proxy_buffering off;" ngLoc.extraConfig;
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
