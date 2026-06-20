# shellcheck shell=bash
# Kubernetes Helm workload (OrbStack's "Battery: Kubernetes"): on a fresh
# cluster, install Traefik and Grafana via Helm, then sample power over a steady
# window. Reports install-to-ready time and average CPU power.
BENCH_NAME="helm_workload"

POWER_WINDOW="${BENCH_POWER_WINDOW:-120}"   # OrbStack used 600s; default 120s

hc() {
  helm --kubeconfig "$ENGINE_KUBECONFIG" ${ENGINE_KCTX:+--kube-context "$ENGINE_KCTX"} "$@"
}

bench_main() {
  require kubectl
  require helm

  log "[$ENGINE] helm_workload: bringing up cluster…"
  engine_warm_prep
  engine_k8s_create
  wait_pods_ready kube-system 1 || warn "kube-system slow to settle"

  helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1 || true
  helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true

  local t0 t1
  t0=$(now_ms)
  hc upgrade --install traefik traefik/traefik -n traefik --create-namespace --wait \
     --timeout "${READY_TIMEOUT}s" >/dev/null 2>&1 || warn "traefik install slow/failed"
  hc upgrade --install grafana grafana/grafana -n grafana --create-namespace --wait \
     --timeout "${READY_TIMEOUT}s" >/dev/null 2>&1 || warn "grafana install slow/failed"
  t1=$(now_ms)
  emit_result "$BENCH_NAME" steady install_to_ready "$((t1 - t0))" ms
  ok "[$ENGINE] helm install→ready: $((t1 - t0)) ms"

  # steady-state power while the workload idles (the OrbStack battery metric)
  power_window "$POWER_WINDOW" "$BENCH_NAME" steady

  hc uninstall grafana -n grafana >/dev/null 2>&1 || true
  hc uninstall traefik -n traefik >/dev/null 2>&1 || true
  engine_k8s_destroy
}
