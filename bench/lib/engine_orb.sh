# shellcheck shell=bash
# OrbStack engine adapter. OrbStack has a single built-in Kubernetes cluster
# (context "orbstack", kubeconfig ~/.orbstack/k8s/config.yml) toggled with
# `orb start k8s` / `orb stop k8s`, and a docker context "orbstack".

ORB_BIN="${ORB_BIN:-orb}"
ORB_KUBECONFIG="${ORB_KUBECONFIG:-$HOME/.orbstack/k8s/config.yml}"

engine_label() { echo "orbstack"; }

engine_docker_context() { echo "orbstack"; }

engine_docker_up() {
  "$ORB_BIN" start >/dev/null 2>&1 || die "orb start failed"
}

# Cold: stop k8s and prune the (shared) image store so images are re-fetched.
# Caveat vs k3c: OrbStack keeps one persistent cluster + shared image store; a
# true wipe is `orbctl reset` (destroys everything), which we deliberately avoid.
# This clears docker/k8s images but not the k8s data dir.
engine_cold_prep() {
  "$ORB_BIN" stop k8s >/dev/null 2>&1 || true
  "$ORB_BIN" start >/dev/null 2>&1 || true
  docker --context orbstack system prune -af >/dev/null 2>&1 || true
}

# Warm: make sure the cluster has run once so its images are cached, then stop.
engine_warm_prep() {
  "$ORB_BIN" start k8s >/dev/null 2>&1 || die "orb start k8s failed"
  "$ORB_BIN" stop k8s >/dev/null 2>&1 || true
}

engine_k8s_create() {
  "$ORB_BIN" stop k8s >/dev/null 2>&1 || true
  "$ORB_BIN" start k8s >/dev/null 2>&1 || die "orb start k8s failed"
  ENGINE_KUBECONFIG="$ORB_KUBECONFIG"
  ENGINE_KCTX="orbstack"
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() {
  "$ORB_BIN" stop k8s >/dev/null 2>&1 || true
}
