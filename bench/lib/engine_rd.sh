# shellcheck shell=bash
# Rancher Desktop engine adapter. RD ships rdctl inside the app bundle (not on
# PATH by default). It runs a single k3s-based cluster (kube context
# "rancher-desktop") controlled via `rdctl start` / `rdctl shutdown`, and binds
# host :80/:443 via its ingress — so it is mutually exclusive with k3c/orb too.
#
# NOTE: starting Rancher Desktop launches its Electron app + VM and is slow;
# the adapter uses a long timeout. Validate live before trusting numbers.

RDCTL="${RDCTL:-/Applications/Rancher Desktop.app/Contents/Resources/resources/darwin/bin/rdctl}"
RD_OP_TIMEOUT="${RD_OP_TIMEOUT:-600}"

engine_label() { echo "rancher"; }
engine_addons() { echo "coredns local-path-provisioner"; }  # k3s default set, like k3c
engine_docker_context() { echo "rancher-desktop"; }

_rd() {
  local out rc
  out="$(timeout "$RD_OP_TIMEOUT" "$RDCTL" "$@" 2>&1)"; rc=$?
  [ $rc -eq 124 ] && warn "rdctl $* timed out after ${RD_OP_TIMEOUT}s"
  [ $rc -ne 0 ] && printf '%s\n' "$out" | tail -5 >&2
  return $rc
}

_rd_running() { "$RDCTL" info >/dev/null 2>&1; }

engine_docker_up() { _rd start >/dev/null || die "rdctl start failed"; }

# RD keeps one persistent cluster + shared image store (like orb): cold = a full
# shutdown+start, warm = already running.
engine_cold_prep() { _rd shutdown >/dev/null 2>&1 || true; sleep 2; }
engine_warm_prep() { _rd start >/dev/null 2>&1 || true; }

engine_k8s_create() {
  # start RD with kubernetes enabled; waits until the backend is up.
  _rd start --kubernetes.enabled=true >/dev/null || die "rdctl start (k8s) failed/timed out"
  ENGINE_KUBECONFIG="$HOME/.kube/config"
  ENGINE_KCTX="rancher-desktop"
  export ENGINE_KUBECONFIG ENGINE_KCTX
}

engine_k8s_destroy() { _rd shutdown >/dev/null 2>&1 || true; }
engine_stop_all()   { _rd shutdown >/dev/null 2>&1 || true; }

# Resume benchmark: RD has no per-cluster suspend; shutdown+start is the analog.
engine_suspend() { _rd shutdown >/dev/null || die "rdctl shutdown failed"; }
engine_resume()  { _rd start >/dev/null || die "rdctl start failed"; }
