# shellcheck shell=bash
# Resume benchmark: bring a cluster up (untimed), release it (suspend-to-disk for
# k3c; k8s stop for orb; shutdown for RD), then TIME restoring it until the
# usable addons are Ready again. This is the fair counterpart to the create
# benchmark — it shows the "get my cluster back" path, where k3c's snapshot/
# suspend model is meant to shine, without hiding the slow from-scratch create.
BENCH_NAME="resume"

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

bench_main() {
  require kubectl
  type engine_suspend >/dev/null 2>&1 || { warn "[$ENGINE] no suspend/resume support; skipping"; return 0; }

  log "[$ENGINE] resume: bringing cluster up (untimed)…"
  engine_warm_prep
  engine_k8s_create
  _wait_addons || { warn "[$ENGINE] cluster not ready before suspend"; engine_k8s_destroy; return 1; }

  log "[$ENGINE] suspending/releasing…"
  engine_suspend

  power_begin
  local t0 t1
  t0=$(now_ms)
  engine_resume
  if ! _wait_addons; then
    power_end "$BENCH_NAME" restore
    warn "[$ENGINE] addons not ready after resume within ${READY_TIMEOUT}s"
    engine_k8s_destroy
    return 1
  fi
  t1=$(now_ms)
  power_end "$BENCH_NAME" restore

  emit_result "$BENCH_NAME" restore resume_time "$((t1 - t0))" ms
  ok "[$ENGINE] resume: $((t1 - t0)) ms"
  engine_k8s_destroy
}
