# shellcheck shell=bash
# OrbStack engine adapter. OrbStack has a single built-in Kubernetes cluster
# (context "orbstack", kubeconfig ~/.orbstack/k8s/config.yml) toggled with
# `orb start k8s` / `orb stop k8s`, and a docker context "orbstack".

ORB_BIN="${ORB_BIN:-orb}"
ORB_KUBECONFIG="${ORB_KUBECONFIG:-$HOME/.orbstack/k8s/config.yml}"
ORB_OP_TIMEOUT="${ORB_OP_TIMEOUT:-240}"   # cap orb start/stop so a hang surfaces

engine_label() { echo "orbstack"; }

# OrbStack's built-in cluster ships only coredns + local-path-provisioner — no
# metrics-server — so the empty_cluster timed set is the common baseline both
# engines have. (k3c additionally runs metrics-server; see engine_k3c.sh.)
engine_addons() { echo "coredns local-path-provisioner"; }

engine_docker_context() { echo "orbstack"; }

# _orb <args...>: run an orb command under a timeout, surfacing its error tail.
_orb() {
  local out rc
  out="$(timeout "$ORB_OP_TIMEOUT" "$ORB_BIN" "$@" 2>&1)"; rc=$?
  [ $rc -eq 124 ] && warn "orb $* timed out after ${ORB_OP_TIMEOUT}s"
  [ $rc -ne 0 ] && printf '%s\n' "$out" | tail -5 >&2
  return $rc
}

# _orb_start_vm: bring the OrbStack VM up, retrying — a full `orb stop` followed
# quickly by `orb start` can report "start VM: timed out" on the first attempt.
_orb_start_vm() {
  local n=0
  while [ "$("$ORB_BIN" status 2>/dev/null)" != "Running" ] && [ $n -lt 3 ]; do
    _orb start >/dev/null || true
    sleep 2
    n=$((n + 1))
  done
  [ "$("$ORB_BIN" status 2>/dev/null)" = "Running" ]
}

engine_docker_up() { _orb_start_vm || die "orb VM did not start"; }

# OrbStack has ONE persistent cluster + shared image store; there is no per-run
# fresh cluster and no non-destructive image wipe (`orbctl reset` would, but we
# won't). So cold vs warm here differs in VM state, not images:
#   cold  = OrbStack fully stopped -> create times VM boot + k8s start + addons
#   warm  = VM already up, k8s down -> create times only k8s start + addons
# (k3c builds a fresh VM every time, so its cold≈warm — note the asymmetry.)
engine_cold_prep() { _orb stop >/dev/null 2>&1 || true; sleep 2; }
engine_warm_prep() {
  _orb_start_vm || true
  _orb start k8s >/dev/null 2>&1 || true
  _orb stop k8s  >/dev/null 2>&1 || true
}

# Times VM boot (cold) or just k8s start (warm), so the metric is comparable to
# k3c's create-from-scratch.
engine_k8s_create() {
  _orb_start_vm || die "orb VM did not start"
  _orb start k8s >/dev/null || die "orb start k8s failed/timed out"
  ENGINE_KUBECONFIG="$ORB_KUBECONFIG"
  ENGINE_KCTX="orbstack"
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() { _orb stop k8s >/dev/null || true; }

# Fully stop OrbStack so it releases host :443 (and :80) for the other engine.
engine_stop_all() { _orb stop >/dev/null 2>&1 || true; }
