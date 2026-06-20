# shellcheck shell=bash
# k3c engine adapter. Drives k3s clusters on Apple's container runtime.
#
# Tunables (env):
#   K3C_BIN        k3c binary (default: k3c on PATH)
#   BENCH_CLUSTER  cluster name for k8s benchmarks (default: bench)

K3C_BIN="${K3C_BIN:-k3c}"
BENCH_CLUSTER="${BENCH_CLUSTER:-bench}"

engine_label() { echo "k3c"; }

engine_docker_context() { echo "k3c"; }

engine_docker_up() {
  "$K3C_BIN" docker up >/dev/null 2>&1 || die "k3c docker up failed"
}

# Cold: remove the cluster and empty the shared pull-through cache so registry
# blobs are re-fetched. (k3s system images are bundled in the node image, so the
# cold/warm delta for the empty cluster is mostly VM + k3s start, not pulls.)
engine_cold_prep() {
  "$K3C_BIN" cluster delete "$BENCH_CLUSTER" --force >/dev/null 2>&1 || true
  "$K3C_BIN" pull-cache clear >/dev/null 2>&1 || true
}

# Warm: ensure the cluster image + caches are present by creating once and
# deleting, leaving the pull cache and node image populated.
engine_warm_prep() {
  "$K3C_BIN" cluster delete "$BENCH_CLUSTER" --force >/dev/null 2>&1 || true
  "$K3C_BIN" cluster create "$BENCH_CLUSTER" >/dev/null 2>&1 || die "warm prep create failed"
  "$K3C_BIN" cluster delete "$BENCH_CLUSTER" --force >/dev/null 2>&1 || true
}

# Create the cluster and wire kubectl to it. Timed by the caller.
engine_k8s_create() {
  "$K3C_BIN" cluster delete "$BENCH_CLUSTER" --force >/dev/null 2>&1 || true
  "$K3C_BIN" cluster create "$BENCH_CLUSTER" >/dev/null 2>&1 || die "k3c cluster create failed"
  ENGINE_KUBECONFIG="$(mktemp -t bench-kubeconfig)"
  "$K3C_BIN" kubeconfig get "$BENCH_CLUSTER" > "$ENGINE_KUBECONFIG" 2>/dev/null \
    || die "k3c kubeconfig get failed"
  ENGINE_KCTX=""   # standalone kubeconfig -> use its current-context
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() {
  "$K3C_BIN" cluster delete "$BENCH_CLUSTER" --force >/dev/null 2>&1 || true
  [ -n "${ENGINE_KUBECONFIG:-}" ] && rm -f "$ENGINE_KUBECONFIG" || true
}
