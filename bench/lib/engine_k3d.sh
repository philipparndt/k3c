# shellcheck shell=bash
# k3d-on-OrbStack engine adapter. k3d runs k3s clusters inside Docker containers;
# here the Docker backend is OrbStack (a very common dev setup). k3d itself does
# not bind host :443 (its loadbalancer maps to a chosen port only), so it
# coexists with OrbStack's docker engine — but OrbStack's *own* k8s must be off,
# and k3c must be stopped, when k3d runs.

ORB_BIN="${ORB_BIN:-orb}"
K3D_BIN="${K3D_BIN:-k3d}"
BENCH_CLUSTER="${BENCH_CLUSTER:-bench}"
K3D_OP_TIMEOUT="${K3D_OP_TIMEOUT:-300}"

engine_label() { echo "k3d"; }
engine_addons() { echo "coredns local-path-provisioner"; }  # k3d default k3s set
engine_docker_context() { echo "orbstack"; }

_orbsock() {
  docker context inspect orbstack -f '{{.Endpoints.docker.Host}}' 2>/dev/null \
    || echo "unix://$HOME/.orbstack/run/docker.sock"
}
_orb_up() { [ "$("$ORB_BIN" status 2>/dev/null)" = "Running" ] || "$ORB_BIN" start >/dev/null 2>&1 || true; }

# run k3d against OrbStack's docker, under a timeout, surfacing errors.
_k3d() {
  local out rc
  out="$(DOCKER_HOST="$(_orbsock)" timeout "$K3D_OP_TIMEOUT" "$K3D_BIN" "$@" 2>&1)"; rc=$?
  [ $rc -eq 124 ] && warn "k3d $* timed out after ${K3D_OP_TIMEOUT}s"
  [ $rc -ne 0 ] && printf '%s\n' "$out" | tail -5 >&2
  return $rc
}

engine_docker_up() { _orb_up; }

engine_cold_prep() { _orb_up; _k3d cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true; }
engine_warm_prep() { _orb_up; _k3d cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true; }

engine_k8s_create() {
  _orb_up
  _k3d cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true
  _k3d cluster create "$BENCH_CLUSTER" --wait --timeout "${READY_TIMEOUT}s" >/dev/null \
    || die "k3d cluster create failed"
  ENGINE_KUBECONFIG="$(mktemp -t bench-kubeconfig)"
  DOCKER_HOST="$(_orbsock)" "$K3D_BIN" kubeconfig get "$BENCH_CLUSTER" > "$ENGINE_KUBECONFIG" 2>/dev/null \
    || die "k3d kubeconfig get failed"
  ENGINE_KCTX=""
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() {
  _k3d cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true
  [ -n "${ENGINE_KUBECONFIG:-}" ] && rm -f "$ENGINE_KUBECONFIG" || true
}

# Free the host for native engines: drop the k3d cluster and stop OrbStack.
engine_stop_all() {
  _k3d cluster delete "$BENCH_CLUSTER" >/dev/null 2>&1 || true
  "$ORB_BIN" stop >/dev/null 2>&1 || true
}

# Resume benchmark: k3d stop/start halts and restarts the cluster containers.
engine_suspend() { _k3d cluster stop "$BENCH_CLUSTER" >/dev/null || die "k3d cluster stop failed"; }
engine_resume()  { _k3d cluster start "$BENCH_CLUSTER" >/dev/null || die "k3d cluster start failed"; }
