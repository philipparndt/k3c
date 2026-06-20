#!/usr/bin/env bash
# Instrument `k3c cluster create` to see where the ~50s goes.
#
# k3c's logger prints elapsed seconds as a "[  NN]" prefix, so we parse known
# phase log lines to build a timeline, and additionally time (externally) how
# long the default addons take to become Ready *after* create returns — the
# benchmark's time_to_ready includes that tail.
#
#   ./instrument_create.sh [iterations]   (default 1)
set -euo pipefail

BENCH_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$BENCH_ROOT/lib/common.sh"

K3C_BIN="${K3C_BIN:-k3c}"
CLUSTER="${BENCH_CLUSTER:-bench}"
ADDONS="${EMPTY_ADDONS:-coredns local-path-provisioner metrics-server}"
ITERS="${1:-1}"

# Phase markers: "substring|label", in create() order. The elapsed seconds at
# each marker, diffed against the next, give per-phase durations.
MARKERS=(
  "starting host daemons|spawn host daemons"
  "starting local registry|start registry"
  "local registry already running|start registry"
  "preparing node config|prepare node config (registries/CA)"
  "starting k3s server|boot VM + launch k3s"
  "waiting for kubeconfig|k3s API up (kubeconfig written)"
  "merging kubeconfig|merge kubeconfig"
  "waiting for node to become Ready|node registration"
  "configuring CoreDNS|node Ready -> coreDNS setup"
  "cluster is up|create() returns"
)

free_443() { orb stop >/dev/null 2>&1 || true; "$K3C_BIN" cluster delete "$CLUSTER" >/dev/null 2>&1 || true; }

# elapsed_of <logfile> <substring>: first "[NN]" elapsed seconds on a line
# containing substring, or empty.
elapsed_of() {
  awk -v pat="$2" '
    index($0, pat) { if (match($0, /\[[ ]*[0-9]+\]/)) {
      s=substr($0, RSTART+1, RLENGTH-2); gsub(/ /,"",s); print s; exit } }
  ' "$1"
}

run_one() {
  local i="$1" log; log="$(mktemp -t k3c-create-log)"
  log_phase() { :; }
  log "iter $i: creating '$CLUSTER' (orb stopped, 443 free)…"
  free_443

  local t0 t_create t_ready
  t0=$(now_ms)
  "$K3C_BIN" cluster create "$CLUSTER" >"$log" 2>&1 || { tail -8 "$log" >&2; die "create failed"; }
  t_create=$(now_ms)

  # wait for the addon deployments after create returns
  local kc_file; kc_file="$(mktemp -t k3c-kcfg)"
  "$K3C_BIN" kubeconfig get "$CLUSTER" >"$kc_file" 2>/dev/null || die "kubeconfig get failed"
  local deadline=$(( $(date +%s) + 300 ))
  while :; do
    local all=1
    for d in $ADDONS; do
      kubectl --kubeconfig "$kc_file" -n kube-system rollout status "deploy/$d" --timeout=3s >/dev/null 2>&1 || all=0
    done
    [ "$all" = 1 ] && break
    [ "$(date +%s)" -lt "$deadline" ] || { warn "addons not ready in 300s"; break; }
    sleep 1
  done
  t_ready=$(now_ms)

  echo
  printf '── iter %s phase timeline (create wall: %ss, total incl. addons: %ss) ──\n' \
    "$i" "$(( (t_create - t0) / 1000 ))" "$(( (t_ready - t0) / 1000 ))"

  # build the per-phase table from log elapsed markers
  local prev_e=0 prev_label="create() start"
  {
    printf 'PHASE\tEND@s\tΔs\n'
    for m in "${MARKERS[@]}"; do
      local sub="${m%%|*}" label="${m##*|}" e
      e="$(elapsed_of "$log" "$sub")"
      [ -z "$e" ] && continue
      printf '%s\t%s\t%s\n' "$prev_label → $label" "$e" "$((e - prev_e))"
      prev_e="$e"; prev_label="$label"
    done
    # tail: create return -> addons ready (wall clock, seconds)
    printf '%s\t%s\t%s\n' "create() returns → addons Ready" \
      "$(( (t_ready - t0) / 1000 ))" "$(( (t_ready - t_create) / 1000 ))"
  } | column -t -s $'\t'

  rm -f "$log" "$kc_file"
  "$K3C_BIN" cluster delete "$CLUSTER" >/dev/null 2>&1 || true
}

require kubectl
for i in $(seq 1 "$ITERS"); do run_one "$i"; done
ok "done. (run k3c daemons restart afterwards if you use a persistent cluster)"
