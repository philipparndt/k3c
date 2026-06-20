# shellcheck shell=bash
# k3c engine adapter. Drives k3s clusters on Apple's container runtime.
#
# Tunables (env):
#   K3C_BIN        k3c binary (default: k3c on PATH)
#   BENCH_CLUSTER  cluster name for k8s benchmarks (default: bench)

K3C_BIN="${K3C_BIN:-k3c}"
BENCH_CLUSTER="${BENCH_CLUSTER:-bench}"

engine_label() { echo "k3c"; }

# Default control-plane addons k3s ships (used by the empty_cluster benchmark).
engine_addons() { echo "coredns local-path-provisioner metrics-server"; }

engine_docker_context() { echo "k3c"; }

# _k3c <args...>: run k3c, capturing combined output; on failure print the tail
# so errors are visible (the harness otherwise runs commands quietly).
_k3c() {
  local out rc
  out="$("$K3C_BIN" "$@" 2>&1)"; rc=$?
  if [ $rc -ne 0 ]; then
    printf '%s\n' "$out" | tail -5 >&2
  fi
  return $rc
}

# _k3c_delete: delete the bench cluster if present (no-op if it isn't).
_k3c_delete() { "$K3C_BIN" cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true; }

engine_docker_up() {
  _k3c docker up >/dev/null || die "k3c docker up failed"
}

# Cold: remove the cluster and empty the shared pull-through cache so registry
# blobs are re-fetched. (k3s system images are bundled in the node image, so the
# cold/warm delta for the empty cluster is mostly VM + k3s start, not pulls.)
engine_cold_prep() {
  _k3c_delete
  "$K3C_BIN" pull-cache clear >/dev/null 2>&1 || true
}

# Warm: ensure the cluster image + caches are present by creating once and
# deleting, leaving the pull cache and node image populated.
engine_warm_prep() {
  _k3c_delete
  _k3c cluster create "$BENCH_CLUSTER" >/dev/null || die "warm prep create failed"
  _k3c_delete
}

# Create the cluster and wire kubectl to it. Timed by the caller.
engine_k8s_create() {
  _k3c_delete
  _k3c cluster create "$BENCH_CLUSTER" >/dev/null || die "k3c cluster create failed"
  ENGINE_KUBECONFIG="$(mktemp -t bench-kubeconfig)"
  "$K3C_BIN" kubeconfig get "$BENCH_CLUSTER" > "$ENGINE_KUBECONFIG" 2>/dev/null \
    || die "k3c kubeconfig get failed"
  ENGINE_KCTX=""   # standalone kubeconfig -> use its current-context
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() {
  _k3c_delete
  [ -n "${ENGINE_KUBECONFIG:-}" ] && rm -f "$ENGINE_KUBECONFIG" || true
}
