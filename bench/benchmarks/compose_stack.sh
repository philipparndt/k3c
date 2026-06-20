# shellcheck shell=bash
# Compose stack (OrbStack's "Battery: Supabase/Sentry"): bring up a multi-service
# docker compose stack, time until up, then sample steady-state power. Runs
# against the engine's docker context.
#
# Defaults to self-hosted Supabase (a clean `docker compose up`). Sentry's
# self-hosted repo needs its ./install.sh and is far heavier; point the vars at
# it if you want that instead.
BENCH_NAME="compose_stack"

COMPOSE_REPO="${COMPOSE_REPO:-https://github.com/supabase/supabase}"
COMPOSE_REF="${COMPOSE_REF:-master}"
COMPOSE_SUBDIR="${COMPOSE_SUBDIR:-docker}"        # dir containing docker-compose.yml
POWER_WINDOW="${BENCH_POWER_WINDOW:-120}"

_dc() { docker --context "$(engine_docker_context)" compose "$@"; }

bench_main() {
  require docker
  require git
  engine_docker_up

  local work; work="$(mktemp -d -t bench-compose)"
  log "[$ENGINE] cloning $COMPOSE_REPO@$COMPOSE_REF…"
  git clone --quiet --filter=blob:none --depth 1 --branch "$COMPOSE_REF" \
    "$COMPOSE_REPO" "$work/src" 2>/dev/null \
    || git clone --quiet --filter=blob:none "$COMPOSE_REPO" "$work/src" \
    || die "clone failed"
  local dir="$work/src/$COMPOSE_SUBDIR"
  [ -f "$dir/docker-compose.yml" ] || [ -f "$dir/compose.yaml" ] \
    || die "no compose file in $COMPOSE_SUBDIR"

  # Supabase ships .env.example; most stacks need an env file present.
  [ -f "$dir/.env" ] || { [ -f "$dir/.env.example" ] && cp "$dir/.env.example" "$dir/.env"; } || true

  log "[$ENGINE] compose up (pull + start)…"
  local t0 t1
  t0=$(now_ms)
  if ( cd "$dir" && _dc up -d --wait --wait-timeout "$READY_TIMEOUT" >/dev/null 2>&1 ); then
    t1=$(now_ms)
    emit_result "$BENCH_NAME" up time_to_up "$((t1 - t0))" ms
    ok "[$ENGINE] compose up: $((t1 - t0)) ms"
    power_window "$POWER_WINDOW" "$BENCH_NAME" steady
  else
    warn "[$ENGINE] compose up failed/slow; check stack requirements"
  fi

  ( cd "$dir" && _dc down -v --remove-orphans >/dev/null 2>&1 ) || true
  rm -rf "$work"
}
