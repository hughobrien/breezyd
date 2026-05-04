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
  };

  config = lib.mkIf cfg.enable {
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
    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [
      (lib.toInt (lib.last (lib.splitString ":" (cfg.settings.daemon.listen or "127.0.0.1:9876"))))
    ];
  };
}
