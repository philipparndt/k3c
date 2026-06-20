#!/usr/bin/env bash
# k3c vs OrbStack benchmark runner.
#
#   ./run.sh [--engines k3c,orb] [--benchmarks empty,helm,pull,build,compose]
#            [--variants cold,warm] [--iterations N] [--power-window SECS]
#            [--no-power] [--ready-timeout SECS]
#
# Results are written to results/<run-id>/results.jsonl and summarized at the end.
set -euo pipefail

BENCH_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export BENCH_ROOT
source "$BENCH_ROOT/lib/common.sh"

# ---- defaults --------------------------------------------------------------
ENGINES="k3c,orb"
BENCHMARKS="empty,helm,pull"
export BENCH_VARIANTS="cold warm"
ITERATIONS=1
export BENCH_POWER=1
export BENCH_POWER_WINDOW=120
export READY_TIMEOUT=300

# ---- bench alias -> file ---------------------------------------------------
bench_file() {
  case "$1" in
    empty)   echo empty_cluster ;;
    helm)    echo helm_workload ;;
    pull)    echo image_pull ;;
    resume)  echo resume ;;
    build)   echo docker_build ;;
    compose) echo compose_stack ;;
    *) die "unknown benchmark: $1 (empty|helm|pull|resume|build|compose)" ;;
  esac
}
engine_file() {
  case "$1" in
    k3c)             echo engine_k3c ;;
    orb|orbstack)    echo engine_orb ;;
    rd|rancher)      echo engine_rd ;;
    *) die "unknown engine: $1 (k3c|orb|rd)" ;;
  esac
}

# every engine that may hold host :443 — quiesced before another engine runs
ALL_ENGINES="k3c orb rd"

# ---- args ------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --engines)       ENGINES="$2"; shift 2 ;;
    --benchmarks)    BENCHMARKS="$2"; shift 2 ;;
    --variants)      export BENCH_VARIANTS="${2//,/ }"; shift 2 ;;
    --iterations)    ITERATIONS="$2"; shift 2 ;;
    --power-window)  export BENCH_POWER_WINDOW="$2"; shift 2 ;;
    --no-power)      export BENCH_POWER=0; shift ;;
    --ready-timeout) export READY_TIMEOUT="$2"; shift 2 ;;
    -h|--help)       sed -n '2,12p' "$0"; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

require jq
require kubectl

RUN_ID="$(date +%Y%m%d-%H%M%S)"
export RUN_ID
RUN_DIR="$RESULTS_DIR/$RUN_ID"
mkdir -p "$RUN_DIR"
export BENCH_RESULT_FILE="$RUN_DIR/results.jsonl"
: > "$BENCH_RESULT_FILE"

# ---- environment capture ---------------------------------------------------
{
  echo "{"
  echo "  \"run\": \"$RUN_ID\","
  echo "  \"chip\": \"$(sysctl -n machdep.cpu.brand_string 2>/dev/null)\","
  echo "  \"macos\": \"$(sw_vers -productVersion 2>/dev/null) ($(sw_vers -buildVersion 2>/dev/null))\","
  echo "  \"k3c\": \"$(k3c version 2>/dev/null | head -1)\","
  echo "  \"orb\": \"$(orb version 2>/dev/null | head -1)\","
  echo "  \"engines\": \"$ENGINES\", \"benchmarks\": \"$BENCHMARKS\","
  echo "  \"variants\": \"$BENCH_VARIANTS\", \"iterations\": $ITERATIONS,"
  echo "  \"power\": $BENCH_POWER, \"power_window_s\": $BENCH_POWER_WINDOW"
  echo "}"
} > "$RUN_DIR/env.json"
log "run $RUN_ID  →  $RUN_DIR"

# ---- prime sudo for powermetrics ------------------------------------------
if [ "$BENCH_POWER" = 1 ]; then
  if command -v powermetrics >/dev/null 2>&1; then
    log "power sampling on — priming sudo (powermetrics needs root)…"
    sudo -v || { warn "no sudo; disabling power"; export BENCH_POWER=0; }
  else
    warn "powermetrics not found; disabling power"; export BENCH_POWER=0
  fi
fi

# ---- run -------------------------------------------------------------------
IFS=',' read -r -a engine_list <<< "$ENGINES"
IFS=',' read -r -a bench_list <<< "$BENCHMARKS"

for eng in "${engine_list[@]}"; do
  ef="$(engine_file "$eng")"
  # Host :443 is exclusive: stop the other engines before this one's phase so
  # their daemons don't collide (k3c, OrbStack, and Rancher Desktop all bind 443).
  for other in $ALL_ENGINES; do
    [ "$other" = "$eng" ] && continue
    of="$(engine_file "$other")"
    log "quiescing '$other' (freeing host :443 for '$eng')…"
    ( source "$BENCH_ROOT/lib/common.sh"; source "$BENCH_ROOT/lib/$of.sh"; engine_stop_all ) || true
  done
  for b in "${bench_list[@]}"; do
    bf="$(bench_file "$b")"
    for i in $(seq 1 "$ITERATIONS"); do
      log "=== ${eng} / ${b} (iter $i/$ITERATIONS) ==="
      (
        export ENGINE="$eng"
        source "$BENCH_ROOT/lib/common.sh"
        source "$BENCH_ROOT/lib/power.sh"
        source "$BENCH_ROOT/lib/$ef.sh"
        # canonicalize the engine name for results so the summary matches
        # (--engines may pass "orb", engine_label is "orbstack")
        export ENGINE="$(engine_label)"
        source "$BENCH_ROOT/benchmarks/$bf.sh"
        bench_main
      ) || warn "${eng}/${b} iteration $i failed (continuing)"
    done
  done
done

# ---- summary ---------------------------------------------------------------
echo
log "summary (mean across iterations):"
jq -rs '
  def cell($v): if $v==null then "-" else ($v|floor|tostring) end;
  group_by([.benchmark,.variant,.metric,.engine])
  | map({benchmark:.[0].benchmark, variant:.[0].variant, metric:.[0].metric,
         engine:.[0].engine, unit:.[0].unit,
         mean:(map(.value)|add/length)})
  | group_by([.benchmark,.variant,.metric])
  | (["BENCHMARK","VARIANT","METRIC","k3c","orbstack","rancher","UNIT"] | @tsv),
    (.[] | . as $g
     | ($g | map(select(.engine=="k3c"))[0].mean) as $k
     | ($g | map(select(.engine=="orbstack"))[0].mean) as $o
     | ($g | map(select(.engine=="rancher"))[0].mean) as $r
     | [$g[0].benchmark,$g[0].variant,$g[0].metric,
        cell($k), cell($o), cell($r), $g[0].unit]
     | @tsv)
' "$BENCH_RESULT_FILE" | column -t -s $'\t'

echo
ok "done. raw: $BENCH_RESULT_FILE"
