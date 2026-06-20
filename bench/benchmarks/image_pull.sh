# shellcheck shell=bash
# Cold image-pull timing: pull a set of images *into the cluster* (so k3c's
# pull-through cache / registries.yaml path is exercised, not the docker
# sidecar) and time until every pod is Running. Cold = caches cleared; warm =
# images already present.
#
# To measure k3c's pull cache specifically, enable it in your k3c config
# (pullCache.enabled: true) so the node's registries.yaml points at it.
BENCH_NAME="image_pull"

# A handful of medium, independent images. Override with PULL_IMAGES="a b c".
PULL_IMAGES="${PULL_IMAGES:-nginx:1.27 redis:7.4 postgres:16 node:22-bookworm python:3.12}"
NS="bench-pull"

_manifest() {
  local i=0
  for img in $PULL_IMAGES; do
    cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: pull-$i
  namespace: $NS
  labels: {app: pull}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: c
    image: $img
    imagePullPolicy: Always
    command: ["sleep", "3600"]
---
YAML
    i=$((i + 1))
  done
}

_count_images() { set -- $PULL_IMAGES; echo $#; }

_run_variant() {
  local variant="$1" n; n=$(_count_images)
  log "[$ENGINE] image_pull ($variant): $n images"
  if [ "$variant" = cold ]; then engine_cold_prep; else engine_warm_prep; fi
  engine_k8s_create
  wait_pods_ready kube-system 1 || true

  kc create namespace "$NS" >/dev/null 2>&1 || true
  power_begin
  local t0 t1
  t0=$(now_ms)
  _manifest | kc apply -f - >/dev/null 2>&1 || die "apply pull pods failed"
  if ! wait_pods_ready "$NS" "$n"; then
    power_end "$BENCH_NAME" "$variant"
    warn "[$ENGINE] pull pods not all Running within ${READY_TIMEOUT}s"
    kc delete ns "$NS" --wait=false >/dev/null 2>&1 || true
    engine_k8s_destroy
    return 1
  fi
  t1=$(now_ms)
  power_end "$BENCH_NAME" "$variant"
  emit_result "$BENCH_NAME" "$variant" pull_time "$((t1 - t0))" ms
  ok "[$ENGINE] image_pull ($variant): $((t1 - t0)) ms for $n images"

  kc delete ns "$NS" --wait=false >/dev/null 2>&1 || true
  engine_k8s_destroy
}

bench_main() {
  require kubectl
  for v in ${BENCH_VARIANTS:-cold warm}; do
    _run_variant "$v" || true
  done
}
