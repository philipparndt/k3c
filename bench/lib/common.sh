# shellcheck shell=bash
# Common harness for the k3c-vs-OrbStack benchmark suite.
#
# Sourced by run.sh and by each benchmark. Provides logging, timing, JSON result
# emission, and the readiness helpers. The engine adapters (engine_k3c.sh,
# engine_orb.sh) implement a small contract used by the benchmarks:
#
#   engine_label                -> echoes a short engine name ("k3c" | "orbstack")
#   engine_cold_prep            -> clear image/content caches for a cold run
#   engine_warm_prep            -> ensure caches are populated for a warm run
#   engine_k8s_create           -> create/start a cluster; on return `kc` works.
#                                  (The benchmark times this call + readiness.)
#   engine_k8s_destroy          -> delete/stop the cluster
#   engine_docker_context       -> echoes the docker context to build/compose against
#   engine_docker_up            -> ensure that docker engine is running
#
# Engines export, after engine_k8s_create:
#   ENGINE_KUBECONFIG   path to a kubeconfig file
#   ENGINE_KCTX         context name within it (may be empty -> current-context)

set -euo pipefail

# ---- paths -----------------------------------------------------------------
BENCH_ROOT="${BENCH_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
RESULTS_DIR="${RESULTS_DIR:-$BENCH_ROOT/results}"

# ---- logging ---------------------------------------------------------------
_c_dim=$'\033[2m'; _c_red=$'\033[31m'; _c_grn=$'\033[32m'; _c_ylw=$'\033[33m'; _c_rst=$'\033[0m'
log()  { printf '%s[bench]%s %s\n' "$_c_dim" "$_c_rst" "$*" >&2; }
ok()   { printf '%s[ ok ]%s %s\n'  "$_c_grn" "$_c_rst" "$*" >&2; }
warn() { printf '%s[warn]%s %s\n'  "$_c_ylw" "$_c_rst" "$*" >&2; }
die()  { printf '%s[fail]%s %s\n'  "$_c_red" "$_c_rst" "$*" >&2; exit 1; }

# ---- time ------------------------------------------------------------------
# now_ms: wall clock in milliseconds (python3 is guaranteed on macOS via xcode
# CLT; fall back to perl).
now_ms() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import time;print(int(time.time()*1000))'
  else
    perl -MTime::HiRes=time -e 'printf "%d\n", time()*1000'
  fi
}

# ---- result emission -------------------------------------------------------
# emit_result <benchmark> <variant> <metric> <value> <unit>
# Appends one JSON object per line to $BENCH_RESULT_FILE.
emit_result() {
  local benchmark="$1" variant="$2" metric="$3" value="$4" unit="$5"
  [ -n "${BENCH_RESULT_FILE:-}" ] || die "BENCH_RESULT_FILE unset"
  jq -nc \
    --arg run "${RUN_ID:-adhoc}" \
    --arg engine "${ENGINE:-?}" \
    --arg benchmark "$benchmark" \
    --arg variant "$variant" \
    --arg metric "$metric" \
    --argjson value "$value" \
    --arg unit "$unit" \
    --arg ts "$(date -u +%FT%TZ)" \
    '{run:$run,engine:$engine,benchmark:$benchmark,variant:$variant,metric:$metric,value:$value,unit:$unit,ts:$ts}' \
    >> "$BENCH_RESULT_FILE"
  log "result: ${ENGINE:-?}/$benchmark/$variant $metric=$value$unit"
}

# ---- kubectl bound to the active engine ------------------------------------
kc() {
  kubectl --kubeconfig "${ENGINE_KUBECONFIG:?engine kubeconfig unset}" \
    ${ENGINE_KCTX:+--context "$ENGINE_KCTX"} "$@"
}

# wait_rollouts <namespace>: block until every Deployment/DaemonSet in the ns is
# available, up to $READY_TIMEOUT seconds. Used to define "cluster ready".
READY_TIMEOUT="${READY_TIMEOUT:-300}"
wait_for_ready() {
  local ns="$1" deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while :; do
    if kc -n "$ns" rollout status deploy --timeout=5s >/dev/null 2>&1 \
       && kc -n "$ns" rollout status ds --timeout=5s >/dev/null 2>&1; then
      return 0
    fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}

# wait_pods_ready <namespace> <min_count>: block until at least min_count pods in
# the namespace are Ready.
wait_pods_ready() {
  local ns="$1" want="$2" deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while :; do
    local ready
    ready=$(kc -n "$ns" get pods --no-headers 2>/dev/null \
      | awk '{split($2,a,"/"); if(a[1]==a[2] && a[2]>0) c++} END{print c+0}')
    [ "${ready:-0}" -ge "$want" ] && return 0
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}

require() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"; }
