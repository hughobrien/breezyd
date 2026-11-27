# SPDX-License-Identifier: GPL-3.0-or-later
#
# NixOS module for the breezyd Vents Twinfresh ERV daemon.
#
# Use:
#
#   {
#     imports = [ inputs.breezyd.nixosModules.default ];
#     services.breezyd = {
#       enable = true;
#       settings = {
#         daemon.listen = "127.0.0.1:9876";
#         daemon.poll_interval = "30s";
#         devices.playroom = { id = "..."; password = "..."; };
#       };
#     };
#   }
#
# Or, for secrets-managed configs (sops-nix, agenix, etc.), set
# services.breezyd.configFile to a path with mode 0600 owned by the
# breezyd system user; the daemon's loader enforces that mode.

{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.breezyd;

  # The local metrics store, if any: VictoriaMetrics, else Prometheus. Both
  # take remote write at <url>/api/v1/write and answer PromQL at <url>/api/v1.
  vm = config.services.victoriametrics;
  prom = config.services.prometheus;
  store =
    if vm.enable then
      "victoriametrics"
    else if prom.enable then
      "prometheus"
    else
      null;
  storeUrl =
    if store == "victoriametrics" then
      "http://${lib.optionalString (lib.hasPrefix ":" vm.listenAddress) "127.0.0.1"}${vm.listenAddress}"
    else
      "http://${
        if prom.listenAddress == "0.0.0.0" || prom.listenAddress == "" then
          "127.0.0.1"
        else
          prom.listenAddress
      }:${toString prom.port}";
  pushLocal = cfg.prometheus.enable && store != null;
  targets =
    cfg.push // lib.optionalAttrs pushLocal { ${cfg.prometheus.pushName}.url = cfg.prometheus.url; };
  validName = n: builtins.match "[a-zA-Z_][a-zA-Z0-9_]*" n != null;

  # /metrics marks each reading it also pushed with pushed_<name>="true", so
  # the local store's scrape drops the copies it already has; any other
  # scraper keeps them.
  scrapeJob = {
    job_name = cfg.prometheus.jobName;
    scrape_interval = cfg.prometheus.scrapeInterval;
    static_configs = [ { targets = [ (cfg.settings.daemon.listen or "127.0.0.1:9876") ]; } ];
    metric_relabel_configs = lib.optional pushLocal {
      source_labels = [ "pushed_${cfg.prometheus.pushName}" ];
      regex = "true";
      action = "drop";
    };
  };
  tomlFormat = pkgs.formats.toml { };
  generatedConfig = tomlFormat.generate "breezyd.toml" cfg.settings;
in
{
  options.services.breezyd = {
    enable = lib.mkEnableOption "breezyd — Vents Twinfresh Breezy / Elite 160 Pro HRV daemon";

    package = lib.mkOption {
      type = lib.types.package;
      default =
        pkgs.breezyd
          or (throw "services.breezyd.package is unset. Set it explicitly, e.g. `services.breezyd.package = inputs.breezyd.packages.\${pkgs.system}.default;`.");
      defaultText = lib.literalExpression "pkgs.breezyd";
      description = "The breezyd package providing /bin/breezyd and /bin/breezy.";
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = lib.literalExpression ''
        {
          daemon.listen = "127.0.0.1:9876";
          daemon.poll_interval = "30s";
          daemon.discovery = "on-start";
          devices.playroom = {
            id = "BREEZY00000000A0";
            password = "your-protocol-password";
          };
        }
      '';
      description = ''
        TOML settings rendered into the breezyd config file.
        Note: anything you put here ends up world-readable in the Nix
        store. For real device passwords, use `configFile` with a
        secrets-management tool (sops-nix, agenix, etc.) instead.
      '';
    };

    configFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = "/run/secrets/breezyd.toml";
      description = ''
        Path to a pre-existing TOML config file. Takes precedence over
        `settings`. The file must be mode 0600 (the loader refuses
        otherwise) and owned by the breezyd system user.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "breezyd";
      description = "System user the daemon runs as.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "breezyd";
      description = "System group the daemon runs as.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Open the daemon's listen TCP port in the firewall. Off by default
        because the spec'd binding is loopback only; flip on if you bind
        the daemon to a non-loopback address.
      '';
    };

    push = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule {
          options.url = lib.mkOption {
            type = lib.types.str;
            example = "http://metrics.example:8428";
            description = "Base URL of a remote write receiver; breezyd pushes to `<url>/api/v1/write`.";
          };
        }
      );
      default = { };
      example = lib.literalExpression ''{ offsite.url = "http://metrics.example:8428"; }'';
      description = ''
        Remote write targets, by name, each sent every successful poll. /metrics
        labels each reading it also pushed `pushed_<name>="true"`; the scrape
        job on that target's host should drop those (`metric_relabel_configs`,
        action `drop`), as the one this module registers locally does. Names
        must be letters, digits and `_`, not starting with a digit.
      '';
    };

    prometheus = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true and a local metrics store is enabled
          (`services.victoriametrics`, else `services.prometheus`), push to it
          as target `pushName` and register a scrape job for /metrics that
          drops the readings it was pushed. Prometheus needs
          `--web.enable-remote-write-receiver` in its `extraFlags`.
          Targets go in the generated config's `[daemon.push]`; with
          `configFile`, put them there yourself.
        '';
      };
      pushName = lib.mkOption {
        type = lib.types.str;
        default = lib.replaceStrings [ "-" "." ] [ "_" "_" ] config.networking.hostName;
        defaultText = lib.literalExpression "the host name, with - and . as _";
        description = "Target name for the local store (the `pushed_<name>` label).";
      };
      url = lib.mkOption {
        type = lib.types.str;
        default = storeUrl;
        defaultText = lib.literalExpression ''"http://<listen address of services.victoriametrics or services.prometheus>"'';
        description = "Base URL of the local store.";
      };
      jobName = lib.mkOption {
        type = lib.types.str;
        default = "breezyd";
        description = "job_name for the auto-registered scrape, and the job label on pushed series.";
      };
      scrapeInterval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Scrape interval for /metrics.";
      };
    };

    homekit = {
      enable = lib.mkEnableOption ''
        HomeKit bridge that exposes configured Breezy units to Apple Home.
        When `services.breezyd.configFile` is set (the user owns their
        config), this option still adjusts the systemd unit (StateDirectory,
        firewall) but does NOT inject a [homekit] block into the file —
        you must add it yourself
      '';

      port = lib.mkOption {
        type = lib.types.port;
        default = 0;
        description = ''
          TCP port the HAP server binds to. 0 = ephemeral (OS-assigned).
          Pin a port if the firewall needs a fixed hole.
        '';
      };

      bridgeName = lib.mkOption {
        type = lib.types.str;
        default = "breezyd";
        description = "Name shown in iOS during HomeKit pairing.";
      };

      stateDir = lib.mkOption {
        type = lib.types.path;
        default = "/var/lib/breezyd/homekit";
        description = ''
          Directory where the HAP server persists pairing keys + the
          generated PIN. Delete to factory-reset HomeKit pairing.

          Must reside under /var/lib/breezyd (the daemon's
          StateDirectory) to be writable under ProtectSystem=strict.
          If you set this elsewhere, also add the parent path to
          systemd.services.breezyd.serviceConfig.ReadWritePaths.
        '';
      };
    };

    nginx = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true, attach a proxy_pass location to a named nginx
          virtual host that forwards to the daemon's HTTP listener.
          Requires services.nginx.enable = true and a non-null
          services.breezyd.nginx.virtualHost.

          Use this to expose the dashboard on the LAN while keeping the
          daemon bound to the loopback default — nginx is the
          network-facing service, the daemon's full /v1/... API stays
          on 127.0.0.1.
        '';
      };

      virtualHost = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "breezy.home.lan";
        description = ''
          Name of a nginx virtual host to attach the dashboard proxy to.
          The module mounts the location at the vhost root ("/"), so use
          a dedicated vhost (or one without an existing "/" location).
          Sub-path mounting under a shared vhost is not supported by
          this module — configure that yourself if needed.
        '';
      };

      basicAuthFile = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        example = "/run/secrets/breezy-htpasswd";
        description = ''
          Optional path to an nginx-format htpasswd file. When set,
          basic-auth gates access to the dashboard. Use sops-nix /
          agenix to manage this file outside the world-readable Nix
          store.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    services = {
      breezyd.settings.daemon = {
        prometheus_job = lib.mkDefault cfg.prometheus.jobName;
        # The daemon rejects Host names it does not know (DNS-rebinding
        # defence); the proxied vhost name must be one of them.
        allowed_hosts = lib.mkIf (cfg.nginx.enable && cfg.nginx.virtualHost != null) [
          cfg.nginx.virtualHost
        ];
        push = lib.mkIf (targets != { }) (lib.mapAttrs (_: t: lib.mkDefault t.url) targets);
      };

      prometheus.scrapeConfigs = lib.mkIf (pushLocal && store == "prometheus") [ scrapeJob ];
      victoriametrics.prometheusConfig.scrape_configs = lib.mkIf (
        pushLocal && store == "victoriametrics"
      ) [ scrapeJob ];

      # Optional nginx reverse-proxy integration. When enabled alongside
      # services.nginx.enable, attach a proxy_pass location to the named
      # virtual host. The daemon's listen address stays loopback-bound;
      # nginx is the network-facing piece.
      #
      # extraConfig sets the SSE-friendly directives the dashboard's
      # /ui/sse stream needs through a reverse proxy:
      #   - proxy_buffering off: events flush immediately, no batching.
      #   - proxy_cache off:     SSE streams must never be cached.
      #   - proxy_http_version 1.1 + Connection "": opts into the keep-alive
      #     pool that lets long-lived streams stay open.
      #   - proxy_read_timeout 1d: SSE connections idle for the full
      #     keepalive interval (30s); upstream send-loop must not be
      #     terminated by nginx's default 60s read timeout.
      #   - proxy_set_header X-Forwarded-Host $http_host: browsers omit
      #     Sec-Fetch-Site on plain-HTTP LAN origins, so the daemon's
      #     cross-origin check falls back to Origin vs Host; the proxied Host
      #     is the loopback upstream, so the public host:port is forwarded
      #     here. ($http_host keeps a non-default port; Host is deliberately
      #     not set, recommendedProxySettings would send it twice.)
      nginx.virtualHosts = lib.mkIf cfg.nginx.enable {
        ${cfg.nginx.virtualHost} = {
          locations."/" = {
            proxyPass = "http://${cfg.settings.daemon.listen or "127.0.0.1:9876"}";
            basicAuthFile = cfg.nginx.basicAuthFile;
            extraConfig = ''
              proxy_buffering off;
              proxy_cache off;
              proxy_http_version 1.1;
              proxy_set_header Connection "";
              proxy_read_timeout 1d;
              proxy_set_header X-Forwarded-Host $http_host;
            '';
          };
        };
      };
    };

    assertions = [
      {
        # Prometheus refuses the flag repeated and other exporters push too,
        # so the host sets it once; only check it is there.
        assertion =
          !(pushLocal && store == "prometheus")
          || lib.elem "--web.enable-remote-write-receiver" prom.extraFlags;
        message = "services.breezyd.prometheus.enable pushes to Prometheus: add \"--web.enable-remote-write-receiver\" to services.prometheus.extraFlags.";
      }
      {
        assertion = lib.all validName (lib.attrNames targets);
        message = "services.breezyd push target names must be letters, digits and _, not starting with a digit: ${lib.concatStringsSep ", " (lib.attrNames targets)}";
      }
      {
        assertion = !cfg.nginx.enable || cfg.nginx.virtualHost != null;
        message = "services.breezyd.nginx.enable requires services.breezyd.nginx.virtualHost to be set (e.g. \"breezy.home.lan\").";
      }
      {
        assertion = !cfg.nginx.enable || config.services.nginx.enable;
        message = "services.breezyd.nginx.enable requires services.nginx.enable = true.";
      }
    ];

    users.users.${cfg.user} = {
      isSystemUser = true;
      inherit (cfg) group;
      description = "breezyd daemon user";
    };
    users.groups.${cfg.group} = { };

    # Put the `breezy` CLI on every user's PATH alongside the daemon.
    # Same derivation produces both binaries, so this is free; users who
    # enable the daemon almost always want the CLI to talk to it.
    environment.systemPackages = [ cfg.package ];

    # System-wide CLI fallback config. The CLI loader tries
    # ~/.config/breezy/config.toml first, then this file. By writing
    # `[daemon].listen` here we let `breezy ls` Just Work for any user
    # on the host without anyone needing a home-directory config.
    # Contains no passwords, so mode 0644 is fine — and required, so
    # non-root users can read it.
    environment.etc."breezy/config.toml" = {
      mode = "0644";
      text = ''
        # Auto-generated by services.breezyd; do not edit by hand.
        # The CLI reads this when no ~/.config/breezy/config.toml exists,
        # so it knows where the daemon is listening.
        [daemon]
        listen = "${cfg.settings.daemon.listen or "127.0.0.1:9876"}"
      '';
    };

    # If no configFile is provided, materialise settings into a 0600 file
    # in /run/breezyd. The loader checks mode bits, so 0600 is required.
    systemd.tmpfiles.rules = lib.mkIf (cfg.configFile == null) [
      "d /run/breezyd 0750 ${cfg.user} ${cfg.group} -"
    ];

    systemd.services.breezyd =
      let
        effectiveConfigFile =
          if cfg.configFile != null then cfg.configFile else "/run/breezyd/breezyd.toml";
        preStart = lib.optionalString (cfg.configFile == null) ''
          install -m 0600 -o ${cfg.user} -g ${cfg.group} \
            ${generatedConfig} /run/breezyd/breezyd.toml
          ${lib.optionalString cfg.homekit.enable ''
                    cat >> /run/breezyd/breezyd.toml <<'EOF'

            [homekit]
            enabled     = true
            bridge_name = "${cfg.homekit.bridgeName}"
            port        = ${toString cfg.homekit.port}
            state_dir   = "${cfg.homekit.stateDir}"
            EOF
          ''}
        '';
      in
      {
        description = "Vents Twinfresh Breezy / Elite 160 Pro daemon";
        wants = [ "network-online.target" ];
        after = [ "network-online.target" ] ++ lib.optional (store != null) "${store}.service";
        wantedBy = [ "multi-user.target" ];

        serviceConfig = {
          ExecStartPre = lib.mkIf (
            cfg.configFile == null
          ) "+${pkgs.writeShellScript "breezyd-prestart" preStart}";
          ExecStart = "${cfg.package}/bin/breezyd --config ${effectiveConfigFile}";
          User = cfg.user;
          Group = cfg.group;
          Restart = "on-failure";
          RestartSec = "5s";

          # Energy state files live under StateDirectory unconditionally; HomeKit
          # pairing files do too when enabled. systemd creates and chowns
          # /var/lib/breezyd/ to the service user.
          StateDirectory = "breezyd";

          # Hardening — breezyd only needs outbound UDP and an HTTP listener.
          NoNewPrivileges = true;
          ProtectSystem = "strict";
          ProtectHome = true;
          PrivateTmp = true;
          PrivateDevices = true;
          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectKernelLogs = true;
          ProtectControlGroups = true;
          ProtectClock = true;
          ProtectHostname = true;
          ProtectProc = "invisible";
          # AF_NETLINK is required by Go's net.Interfaces() on Linux, which
          # the brutella/hap mDNS responder calls via dnssd.MulticastInterfaces()
          # to discover which interfaces to advertise on. Without netlink the
          # call silently returns an empty list — the HAP server runs, but
          # advertises on zero interfaces, so iPhones never see the bridge.
          # No log, no error; just dead silence on UDP/5353.
          RestrictAddressFamilies = [
            "AF_INET"
            "AF_INET6"
            "AF_UNIX"
            "AF_NETLINK"
          ];
          RestrictNamespaces = true;
          RestrictRealtime = true;
          RestrictSUIDSGID = true;
          LockPersonality = true;
          MemoryDenyWriteExecute = true;
          SystemCallArchitectures = "native";
          SystemCallFilter = [
            "@system-service"
            "~@privileged"
          ];
          ReadWritePaths = lib.mkIf (cfg.configFile == null) [ "/run/breezyd" ];
        };
      };

    # Optional firewall opening; assumes the listen string is host:port TCP.
    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall (
      [ (lib.toInt (lib.last (lib.splitString ":" (cfg.settings.daemon.listen or "127.0.0.1:9876")))) ]
      ++ lib.optional (cfg.homekit.enable && cfg.homekit.port != 0) cfg.homekit.port
    );

    # HomeKit needs mDNS (UDP/5353) for iPhones to discover the bridge.
    # The HAP library does its own mDNS — no avahi needed — but inbound
    # UDP/5353 has to reach the daemon for pairing and ongoing
    # connectivity. Open it whenever openFirewall + homekit are both on.
    networking.firewall.allowedUDPPorts = lib.mkIf (cfg.openFirewall && cfg.homekit.enable) [ 5353 ];
  };
}
