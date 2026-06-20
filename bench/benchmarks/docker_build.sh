# shellcheck shell=bash
# Heavy Docker build (OrbStack's "Heavy Build: PostHog"): clone a repo at a
# pinned commit and build its Dockerfile for arm64 then amd64 serially, timing
# each. Runs against the engine's docker context (k3c sidecar or orbstack).
#
# Defaults reproduce OrbStack's PostHog build; override for a lighter run, e.g.
#   BUILD_REPO=https://github.com/docker-library/hello-world BUILD_DOCKERFILE=Dockerfile
BENCH_NAME="docker_build"

BUILD_REPO="${BUILD_REPO:-https://github.com/PostHog/posthog}"
BUILD_REF="${BUILD_REF:-42f4d6bb}"
BUILD_DOCKERFILE="${BUILD_DOCKERFILE:-production.Dockerfile}"
BUILD_PLATFORMS="${BUILD_PLATFORMS:-linux/arm64 linux/amd64}"

_dk() { docker --context "$(engine_docker_context)" "$@"; }

bench_main() {
  require docker
  require git
  engine_docker_up

  local work; work="$(mktemp -d -t bench-build)"
  log "[$ENGINE] cloning $BUILD_REPO@$BUILD_REF…"
  git clone --quiet --filter=blob:none "$BUILD_REPO" "$work/src" || die "clone failed"
  ( cd "$work/src" && git checkout --quiet "$BUILD_REF" ) || die "checkout $BUILD_REF failed"

  for plat in $BUILD_PLATFORMS; do
    local tag="bench-build:${plat##*/}"
    log "[$ENGINE] building $BUILD_DOCKERFILE for $plat (no cache)…"
    power_begin
    local t0 t1
    t0=$(now_ms)
    if _dk build --no-cache --platform "$plat" -f "$work/src/$BUILD_DOCKERFILE" \
         -t "$tag" "$work/src" >/dev/null 2>&1; then
      t1=$(now_ms)
      power_end "$BENCH_NAME" "${plat##*/}"
      emit_result "$BENCH_NAME" "${plat##*/}" build_time "$((t1 - t0))" ms
      ok "[$ENGINE] build $plat: $((t1 - t0)) ms"
    else
      power_end "$BENCH_NAME" "${plat##*/}"
      warn "[$ENGINE] build failed for $plat (see Dockerfile/platform support)"
    fi
    _dk image rm "$tag" >/dev/null 2>&1 || true
  done

  rm -rf "$work"
}
