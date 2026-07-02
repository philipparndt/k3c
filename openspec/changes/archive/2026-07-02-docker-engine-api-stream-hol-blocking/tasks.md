## 1. Spike S1 — pin the blocking layer and pick the fix (gates implementation)

- [x] 1.1 Reproduce E1 (open `follow=false` logs, don't drain, then `Inspect`) against `~/.config/k3c/docker.sock` with a minimal Go harness inside k3c; confirm the hang and that the same sequence over `tcp://127.0.0.1:<DockerPort>` passes — HUNG over socket, OK (3ms) over loopback
- [x] 1.2 Determine WHERE it blocks — reproduced E1 talking DIRECTLY to `docker-fwd.sock` (the host side of Apple's `--publish-socket`, bypassing `startDockerSocket`'s `splice`): still HUNG → HOL is in Apple's `--publish-socket` bridge, NOT k3c's `splice` (which is per-connection independent)
- [x] 1.3 Verify whether the Apple loopback publish carries hijacked/archive streams — `exec -i cat` returns EMPTY over loopback but echoes correctly over the bridge (hijack dropped); `docker cp` archive PUT works over BOTH
- [x] 1.4 Choose remediation — **B** (route hijacked→bridge, everything else→loopback); C is a no-op (HOL is Apple's, not k3c's), A breaks exec/attach. Recorded in design.md D2

## 2. Implement the fix

- [x] 2.1 Implemented remediation B in `cluster/dockerports.go`: `startDockerSocket` now routes each connection via `routeEngineConn` (peek first HTTP head → hijack/unrecognized to the bridge via `dialDockerEngine`, non-hijack to the HOL-free loopback endpoint). No change needed to `splice`/`dockerfwd` — the HOL was in Apple's bridge, not k3c code
- [x] 2.2 Hijacked exec/attach route over the bridge (carries hijack); archive PUT (`docker cp`) is non-hijack and routes to loopback, which S1.3 confirmed carries archive uploads
- [x] 2.3 Host socket path (`dockerSocketPath`) and `k3c docker env` output unchanged — routing is purely internal to the forwarder

## 3. Regression tests

- [x] 3.1 Added `TestEngineBackpressuredLogsDoNotBlockInspect` in `cluster/dockerports_test.go`: opens an undrained `follow=false` logs stream then a concurrent `Inspect` over the published socket; asserts the Inspect completes < 5s. A HOL-trap bridge is wired in so a regression that routes non-hijack over the bridge would hang the test
- [x] 3.2 Added `TestEngineHijackRoutesToBridge` (exec/attach → full-duplex bridge), `TestEngineHijackFallsBackToLoopbackWhenBridgeAbsent`, and `TestClassifyEngineHead` (covers attach, attach/ws, exec-start, `Connection: Upgrade`, and archive PUT `docker cp` classified non-hijack → loopback which carries it)
- [x] 3.3 `TestEngineBackpressuredLogsDoNotBlockInspect` models the `wait.ForLog` sequence (ContainerLogs-open → undrained → Inspect) in-process; the live redpanda `ForLog` smoke over the real socket is verified in 4.1

## 4. Verify end-to-end + document

- [x] 4.1 Confirmed: `TestKafkaHarness_ProduceConsumeAndSchemaRegistry` (redpanda.Run v0.43.0) reaches readiness in ~4.3s and passes produce/consume + Schema Registry over the fixed socket (was 60s timeout). The `startKafka` `t.Skip` can be removed (that edit is in the `vehub-budget-management-go` repo, outside this change's edit root — recommended follow-up; its comment blaming a "config-copy handshake" is superseded by this HOL fix)
- [x] 4.2 `k3c doctor` (new build): daemons "running (current build and config)", all listeners up — no regression. Unrelated pre-existing items only (container CLI not on PATH, CIDR/VPN overlap, no cluster). Verified live over the socket: exec `-i` hijack echoes correctly, `docker cp` archive PUT works, E1 completes in 17ms
- [x] 4.3 Updated the `docker-sidecar` capability spec ("Expose the engine to shells and CI" now carries the no-HOL / hijack / archive guarantee + two scenarios) and `ARCHITECTURE.md` §2 (type-based `routeEngineConn` routing, HOL rationale)
