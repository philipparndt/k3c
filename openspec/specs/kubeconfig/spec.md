# kubeconfig Specification

## Purpose

Expose a cluster's kubeconfig for use with `kubectl`, both as raw output and by
merging it into the user's default kubeconfig. This capability owns `k3c
kubeconfig`. The kubeconfig server points at the host-published API port
(`127.0.0.1:<apiport>`), and the context name is `<contextPrefix><name>`.

## Requirements

### Requirement: Print a cluster's kubeconfig

`k3c kubeconfig get [NAME]` SHALL print the named cluster's kubeconfig to
stdout, with the server set to the host-published API address.

#### Scenario: Print kubeconfig

- **WHEN** the user runs `k3c kubeconfig get`
- **THEN** the cluster's kubeconfig is written to stdout pointing at the
  host-published API port

### Requirement: Merge into the default kubeconfig

`k3c kubeconfig merge [NAME]` SHALL merge the cluster's kubeconfig into
`~/.kube/config` and switch the current context to it.

#### Scenario: Merge and switch context

- **WHEN** the user runs `k3c kubeconfig merge`
- **THEN** the cluster's entry is merged into `~/.kube/config` and becomes the
  current context
