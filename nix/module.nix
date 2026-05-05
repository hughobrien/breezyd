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

{ config, lib, pkgs, ... }:

let
  cfg = config.services.breezyd;
  tomlFormat = pkgs.formats.toml { };
  generatedConfig = tomlFormat.generate "breezyd.toml" cfg.settings;
in {
  options.services.breezyd = {
    enable = lib.mkEnableOption "breezyd — Vents Twinfresh Breezy / Elite 160 Pro HRV daemon";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.breezyd or (throw
        "services.breezyd.package is unset. Set it explicitly, e.g. `services.breezyd.package = inputs.breezyd.packages.\${pkgs.system}.default;`.");
      defaultText = lib.literalExpression "pkgs.breezyd";
      description = "The breezyd package providing /bin/breezyd and /bin/breezy.";
    };

    settings = lib.mkOption {
      type = tomlFormat.type;
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

    prometheus = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          When true and `services.prometheus.enable` is also true, register
          a scrape job for breezyd's `/metrics` endpoint automatically.
        '';
      };

      jobName = lib.mkOption {
        type = lib.types.str;
        default = "breezyd";
        description = "Prometheus job_name for the auto-registered scrape.";
      };

      scrapeInterval = lib.mkOption {
        type = lib.types.str;
        default = "30s";
        description = "Scrape interval. Match or exceed the daemon's poll_interval.";
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
    assertions = [
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
      group = cfg.group;
      description = "breezyd daemon user";
    };
    users.groups.${cfg.group} = { };

    # If no configFile is provided, materialise settings into a 0600 file
    # in /run/breezyd. The loader checks mode bits, so 0600 is required.
    systemd.tmpfiles.rules = lib.mkIf (cfg.configFile == null) [
      "d /run/breezyd 0750 ${cfg.user} ${cfg.group} -"
    ];

    systemd.services.breezyd = let
      effectiveConfigFile =
        if cfg.configFile != null then cfg.configFile
        else "/run/breezyd/breezyd.toml";
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
    in {
      description = "Vents Twinfresh Breezy / Elite 160 Pro daemon";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        ExecStartPre = lib.mkIf (cfg.configFile == null)
          "+${pkgs.writeShellScript "breezyd-prestart" preStart}";
        ExecStart = "${cfg.package}/bin/breezyd --config ${effectiveConfigFile}";
        User = cfg.user;
        Group = cfg.group;
        Restart = "on-failure";
        RestartSec = "5s";

        # Give systemd ownership of /var/lib/breezyd so the daemon can create
        # subdirectories (e.g. the HomeKit state dir) at runtime.
        StateDirectory = lib.mkIf cfg.homekit.enable "breezyd";

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
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" "~@privileged" ];
        ReadWritePaths = lib.mkIf (cfg.configFile == null) [ "/run/breezyd" ];
      };
    };

    # Optional firewall opening; assumes the listen string is host:port TCP.
    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall (
      [ (lib.toInt (lib.last (lib.splitString ":" (cfg.settings.daemon.listen or "127.0.0.1:9876")))) ]
      ++ lib.optional (cfg.homekit.enable && cfg.homekit.port != 0) cfg.homekit.port
    );

    # Auto-wire a Prometheus scrape job when both services are enabled.
    services.prometheus.scrapeConfigs = lib.mkIf
      (cfg.prometheus.enable && config.services.prometheus.enable) [{
        job_name = cfg.prometheus.jobName;
        scrape_interval = cfg.prometheus.scrapeInterval;
        static_configs = [{
          targets = [ (cfg.settings.daemon.listen or "127.0.0.1:9876") ];
        }];
      }];

    # Optional nginx reverse-proxy integration. When enabled alongside
    # services.nginx.enable, attach a proxy_pass location to the named
    # virtual host. The daemon's listen address stays loopback-bound;
    # nginx is the network-facing piece.
    services.nginx.virtualHosts = lib.mkIf cfg.nginx.enable {
      ${cfg.nginx.virtualHost} = {
        locations."/" = {
          proxyPass = "http://${cfg.settings.daemon.listen or "127.0.0.1:9876"}";
          basicAuthFile = cfg.nginx.basicAuthFile;
        };
      };
    };
  };
}
