# shellcheck shell=bash
# Power sampling via macOS powermetrics. Measures system CPU power (mW) over a
# window. This approximates OrbStack's "process energy" metric: they attribute
# energy per-process via kernel sampling; here we sample whole-system CPU power
# and subtract an idle baseline so the number is workload-attributable. Document
# this difference when comparing to OrbStack's published charts.
#
# Requires sudo. run.sh primes the sudo credential cache up front.

POWER_INTERVAL_MS="${POWER_INTERVAL_MS:-1000}"
_POWER_PID=""
_POWER_OUT=""

power_available() {
  command -v powermetrics >/dev/null 2>&1 && sudo -n true 2>/dev/null
}

# _power_cleanup: kill a still-running sampler. Registered on EXIT so a benchmark
# that dies between power_begin and power_end cannot leak a long-lived
# `powermetrics` (it runs with -n 100000 ≈ a day otherwise).
_power_cleanup() {
  [ -n "$_POWER_PID" ] || return 0
  sudo kill "$_POWER_PID" 2>/dev/null || true
  wait "$_POWER_PID" 2>/dev/null || true
  [ -n "$_POWER_OUT" ] && rm -f "$_POWER_OUT" 2>/dev/null || true
  _POWER_PID=""; _POWER_OUT=""
}

# power_begin: start sampling CPU power in the background until power_end.
power_begin() {
  [ "${BENCH_POWER:-1}" = "1" ] || return 0
  if ! power_available; then
    warn "powermetrics/sudo unavailable; skipping power sampling"
    BENCH_POWER=0
    return 0
  fi
  trap _power_cleanup EXIT
  _POWER_OUT="$(mktemp -t bench-power)"
  # -n 0 would run forever; use a very large count and kill on power_end.
  sudo powermetrics --samplers cpu_power -i "$POWER_INTERVAL_MS" -n 100000 \
    > "$_POWER_OUT" 2>/dev/null &
  _POWER_PID=$!
}

# power_end <benchmark> <variant>: stop sampling and emit the average CPU power.
power_end() {
  [ "${BENCH_POWER:-1}" = "1" ] || return 0
  [ -n "$_POWER_PID" ] || return 0
  sudo kill "$_POWER_PID" 2>/dev/null || true
  wait "$_POWER_PID" 2>/dev/null || true
  local avg
  avg=$(awk '/CPU Power:/ {sum+=$3; n++} END{ if(n>0) printf "%.0f", sum/n; else print "0" }' "$_POWER_OUT")
  rm -f "$_POWER_OUT"
  _POWER_PID=""; _POWER_OUT=""
  [ "${avg:-0}" -gt 0 ] && emit_result "$1" "$2" cpu_power "$avg" mW
}

# power_window <seconds> <benchmark> <variant>: sample for a fixed window while
# the workload sits at steady state (OrbStack used 10 minutes; default 120s).
power_window() {
  local secs="$1" bench="$2" variant="$3"
  power_begin
  log "sampling power for ${secs}s (steady state)…"
  sleep "$secs"
  power_end "$bench" "$variant"
}
