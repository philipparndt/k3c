package cluster

import "k3c/config"

// frozen.go implements the frozen snapshot tier: a logical, guest-side
// extract rather than a block-level clone (macOS cannot mount the guest
// ext4, so images cannot be carved out host-side). A frozen snapshot keeps
// every byte of non-reconstructable data — the k3s datastore and ALL
// persistent-volume data — and drops only the container image store, which
// is rehydrated from the pull-cache on thaw. See openspec/specs/snapshots.

// Frozen snapshot layout (under the snapshot dir):
const (
	frozenStateDB    = "frozen-state.db"      // sqlite online backup of k3s datastore
	frozenStorageTar = "frozen-storage.tar"   // /var/lib/rancher/k3s/storage (PVC data)
	frozenCertsTar   = "frozen-certs.tar"      // k3s server TLS + token
	frozenManifestF  = "frozen-images.yaml"    // image-digest closure manifest
)

// frozenManifest records the image closure a frozen snapshot depends on, so
// the closure can be pinned (pinSnapshotImages) and rehydrated on thaw.
type frozenManifest struct {
	Images  []string // image references referenced by the cluster's workloads
	Digests []string // the full closure: manifest + config + layer digests (pinned)
}

// writeFrozenSnapshot performs the guest-side logical extract into dir:
// sqlite online backup of state.db, tar of the persistent-volume storage,
// the k3s server certs/token, and the image-digest manifest. It MUST refuse
// to produce a snapshot missing the persistent-volume data (the correctness
// invariant: never drop non-reconstructable data).
//
// CONTRACT STUB — real implementation is task group 4.
func writeFrozenSnapshot(cfg *config.Config, dir string) error { return nil }

// restoreFrozenSnapshot thaws a frozen snapshot: re-create the datastore and
// persistent-volume data into a fresh cluster, boot cold-equivalent, and
// trigger image rehydration from the pull-cache mirror. Fails clearly if a
// referenced digest is absent from the pull-cache.
//
// CONTRACT STUB — real implementation is task group 5.
func restoreFrozenSnapshot(cfg *config.Config, dir string) error { return nil }
