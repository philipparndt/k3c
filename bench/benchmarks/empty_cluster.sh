# shellcheck shell=bash
# Empty-cluster bring-up: time from `create` to the default control-plane addons
# (coredns, local-path-provisioner, metrics-server) all Ready. Cold vs warm.
#
# Note: k3s ships these addons as bundled airgap images inside the node image, so
# the cold/warm delta here reflects VM boot + k3s start, not registry pulls (the
# pull path is exercised by the image_pull benchmark).
BENCH_NAME="empty_cluster"

# Addon set is engine-specific (OrbStack has no metrics-server); override with
# EMPTY_ADDONS to force a fixed list across engines.
ADDONS="${EMPTY_ADDONS:-$(engine_addons)}"

_wait_addons() {
  local deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while :; do
    local all=1
    for d in $ADDONS; do
      kc -n kube-system rollout status "deploy/$d" --timeout=3s >/dev/null 2>&1 || all=0
    done
    [ "$all" = 1 ] && return 0
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 1
  done
}

_run_variant() {
  local variant="$1"
  log "[$ENGINE] empty_cluster ($variant): preparing…"
  if [ "$variant" = cold ]; then engine_cold_prep; else engine_warm_prep; fi

  power_begin
  local t0 t1
  t0=$(now_ms)
  engine_k8s_create
  if ! _wait_addons; then
    power_end "$BENCH_NAME" "$variant"
    engine_k8s_destroy
    warn "[$ENGINE] addons not ready within ${READY_TIMEOUT}s ($variant)"
    return 1
  fi
  t1=$(now_ms)
  power_end "$BENCH_NAME" "$variant"

  emit_result "$BENCH_NAME" "$variant" time_to_ready "$((t1 - t0))" ms
  ok "[$ENGINE] empty_cluster ($variant): $((t1 - t0)) ms"
  engine_k8s_destroy
}

bench_main() {
  require kubectl
  for v in ${BENCH_VARIANTS:-cold warm}; do
    _run_variant "$v" || true
  done
}
