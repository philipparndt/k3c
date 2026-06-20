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

engine_docker_up() {
  _orb start >/dev/null || die "orb start failed"
}

# OrbStack has ONE persistent cluster with a shared image store; there is no
# per-run fresh cluster and no non-destructive image wipe (that is `orbctl
# reset`, which we will not run). So "cold" and "warm" both measure a k8s
# stop+start — documented in the README. We deliberately do NOT prune images.
engine_cold_prep() { _orb start >/dev/null || true; }
engine_warm_prep() { _orb start k8s >/dev/null || true; _orb stop k8s >/dev/null || true; }

engine_k8s_create() {
  _orb stop k8s >/dev/null || true
  _orb start k8s >/dev/null || die "orb start k8s failed/timed out"
  ENGINE_KUBECONFIG="$ORB_KUBECONFIG"
  ENGINE_KCTX="orbstack"
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() { _orb stop k8s >/dev/null || true; }
