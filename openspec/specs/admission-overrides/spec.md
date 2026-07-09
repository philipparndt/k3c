# admission-overrides Specification

## Purpose

Let production-sized Kubernetes manifests schedule on a single laptop-hosted
node by neutralizing the CPU and memory *requests* that would otherwise make
pods `Pending` with `Insufficient cpu/memory`. This capability owns the
`cluster.ignoreCpuRequests` / `cluster.ignoreMemoryRequests` settings and the
mutating admission webhook that enforces them. The webhook is served from the
host daemons (see [[host-daemons]]) with no in-cluster components, so it needs
no images pulled into the guest and cannot wedge the cluster if the host side
is down.

## Requirements

### Requirement: Strip pod resource requests via a mutating webhook

The cluster SHALL, when `cluster.ignoreCpuRequests` and/or
`cluster.ignoreMemoryRequests` is set, register a URL-mode
`MutatingWebhookConfiguration` that
rewrites the corresponding resource *requests* on every pod create to a
negligible value (CPU `1m`, memory `1Mi`) via a JSON patch, so that a workload
authored for a production-sized cluster schedules onto the single k3c node.
Only the enabled dimensions SHALL be rewritten; resource *limits* SHALL be left
unchanged. The webhook SHALL NOT be registered when neither setting is enabled.

#### Scenario: Production-sized chart schedules on the laptop

- **WHEN** `cluster.ignoreMemoryRequests: true` and a pod requesting several GiB
  of memory is created
- **THEN** the admitted pod's memory request is rewritten to a negligible value
  and the pod schedules onto the node instead of staying `Pending` with
  `Insufficient memory`

#### Scenario: Only enabled dimensions are rewritten

- **WHEN** `cluster.ignoreCpuRequests: true` but `cluster.ignoreMemoryRequests`
  is unset
- **THEN** pod CPU requests are stripped to a negligible value while memory
  requests (and all limits) are left untouched

#### Scenario: No webhook without any override

- **WHEN** neither `cluster.ignoreCpuRequests` nor
  `cluster.ignoreMemoryRequests` is set
- **THEN** no `MutatingWebhookConfiguration` is registered and pod requests are
  admitted unchanged

### Requirement: Webhook is served from the host and cannot wedge the cluster

The webhook endpoint SHALL be served by the host daemons (reachable at the
vmnet gateway address) rather than by an in-cluster deployment, using a
self-signed certificate for the gateway address. It SHALL be configured with
`failurePolicy: Ignore` so that a pod create still succeeds if the host-side
webhook is unavailable, and it SHALL exclude system namespaces so it never
interferes with control-plane pods.

#### Scenario: Cluster still admits pods when the host webhook is down

- **WHEN** the host daemons are not serving the webhook (e.g. stopped) and a pod
  is created
- **THEN** the pod create succeeds unmutated because the webhook's failure
  policy is `Ignore`, rather than being rejected
