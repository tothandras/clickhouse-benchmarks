{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

{
  # https://devenv.sh/packages/
  packages = with pkgs; [
    git
    jq
    yq-go

    # ClickHouse
    clickhouse
  ];

  # https://devenv.sh/languages/
  languages.go.enable = true;

  languages.javascript = {
    enable = true;
    package = pkgs.nodejs-slim_25;
    pnpm.enable = true;
  };

  # Python is here only to host mcp-clickhouse for the Claude Code MCP
  # integration. The harness itself is Go; nothing else uses Python.
  # Installed via uv into the project venv (devenv.sh/languages/python).
  languages.python = {
    enable = true;
    venv.enable = true;
    uv = {
      enable = true;
      sync.enable = true;
    };
  };

  # ClickHouse: project-local server managed by devenv. Default ports
  # (9000 native, 8123 HTTP), default user `default` no password. Started
  # with `devenv up`; data lives under .devenv/state/clickhouse/.
  # https://devenv.sh/services/clickhouse/
  #
  # The bench's per-query CPU/memory probe (bench/runner/results.go) reads
  # from system.query_log, so we enable it explicitly — devenv's minimal
  # default config omits it.
  services.clickhouse = {
    enable = true;
    config = ''
      query_log:
        database: system
        table: query_log
        partition_by: toYYYYMM(event_date)
        flush_interval_milliseconds: 1000
    '';
    usersConfig.users.default = {
      profile = "default";
      networks.ip = "::/0";
      quota = "default";
    };
  };

  cachix.enable = false;

  dotenv.enable = true;

  # Generates .devcontainer.json from this devenv configuration.
  # https://devenv.sh/integrations/codespaces-devcontainer/
  devcontainer.enable = true;

  enterShell = ''
    export PATH="$PWD/node_modules/.bin:$PATH"

    echo "ch-playground dev shell"
    echo "  clickhouse-client: $(clickhouse-client --version 2>/dev/null | head -1)"
    echo "  go:                $(go version 2>/dev/null)"
    echo "  openspec:          $(openspec --version 2>/dev/null || echo missing)"
    echo "  mcp-clickhouse:    $([ -x .devenv/state/venv/bin/mcp-clickhouse ] && echo installed || echo missing) (Claude Code MCP server, configured in .mcp.json)"
  '';

  # https://devenv.sh/tasks/
  tasks = {
    # Install node tooling (OpenSpec CLI, etc.) from pnpm-lock.yaml.
    # `pnpm install --frozen-lockfile` is a no-op when node_modules is up to date,
    # so this is cheap to run on every shell entry and fails loudly on lockfile drift.
    "node:install" = {
      exec = ''
        cd "$DEVENV_ROOT"
        pnpm install --frozen-lockfile --silent
      '';
      before = [ "devenv:enterShell" ];
    };

    # Restore Claude Code skills declared in skills-lock.json by re-running
    # `skills add` for each unique source in the lock. We can't use
    # `skills experimental_install` because it hardcodes `.agents/skills/`
    # and ignores --agent (see skills CLI runInstallFromLock); we need
    # `.claude/skills/` for Claude Code to find them.
    # Only touches the network when at least one locked skill is missing.
    "skills:restore" = {
      exec = ''
        cd "$DEVENV_ROOT"
        [ -f skills-lock.json ] || exit 0
        missing=0
        for name in $(jq -r '.skills | keys[]' skills-lock.json); do
          [ -d ".claude/skills/$name" ] || { missing=1; break; }
        done
        [ "$missing" = "0" ] && exit 0
        for source in $(jq -r '.skills | to_entries[] | .value.source' skills-lock.json | sort -u); do
          pnpm dlx skills@1.5.9 add "$source" --agent claude-code -y
        done
      '';
      after = [ "node:install" ];
      before = [ "devenv:enterShell" ];
    };

    # COGS smoke: run the idle and mixed cells with short (ci) phases and zero
    # rates against the devenv ClickHouse, then assert valid cogs/v1 results.
    # Manual: `devenv tasks run cogs:smoke` (needs `devenv up` ClickHouse).
    # ~13 minutes (two cells x 2m soak + 3m measure + 1m drain + collection).
    "cogs:smoke" = {
      exec = ''
        set -euo pipefail
        cd "$DEVENV_ROOT"
        export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
        out=$(mktemp -d)
        go run ./bench/cmd/bench cogs --cell idle --profile ci --pricing-profile local-zero --preload-rows 0 --results-dir "$out"
        go run ./bench/cmd/bench cogs --cell mixed-5keps-4qps --profile ci --pricing-profile local-zero --preload-rows 500000 --results-dir "$out"
        for f in "$out"/proposal/cogs/*.json; do
          jq -e '.kind == "cogs/v1" and (.errors | length == 0)' "$f" >/dev/null \
            || { echo "FAIL: invalid result or errors recorded: $f"; exit 1; }
        done
        latest=$(ls "$out"/proposal/cogs/*.json | tail -1)
        jq -e '.attribution.coverage > 0
               and .attribution.insert_cpu_sec > 0
               and .attribution.merge_cpu_sec > 0
               and (.attribution.query_cpu_sec | length > 0)
               and (.costs.billed_shape.window_usd // 0) == 0' "$latest" >/dev/null \
          || { echo "FAIL: mixed cell must attribute insert+merge+query CPU at zero rates: $latest"; exit 1; }
        echo "cogs:smoke PASS ($out)"
      '';
    };
  };

  # See full reference at https://devenv.sh/reference/options/
}
