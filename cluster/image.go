package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// nodeImagesDir is where the cluster's images directory (config.ImagesDir) is
// bind-mounted inside the node (cluster.go, the -v at create; virtiofs.go
// re-mounts it after a restore).
const nodeImagesDir = "/var/lib/rancher/k3s/agent/images"

// ImageImport loads an image from the host's `container` image store into
// the cluster under its original name — no registry involved. The image is
// saved as a tarball into the bind-mounted k3s agent/images directory and
// then imported into k3s's containerd from INSIDE the node.
//
// WHY THE IMPORT RUNS INSIDE THE NODE. k3s watches agent/images with inotify
// and imports whatever appears there — on a real disk. The directory here is
// a virtiofs mount, and a file written on the host side raises no inotify
// event in the guest: the tarball sits there, `ls` inside the node shows it,
// k3s never notices, and the previous version of this command waited a
// minute and then blamed a missing mount. (k3s does import the directory at
// START-UP, which is why a tarball left behind would surface on the next
// restart; the tarball is removed again either way.)
//
// WHY THE TAG. The tarball names the image the way the host store does,
// e.g. `my/image:dev`. The kubelet asks the CRI for the NORMALISED reference
// (`docker.io/my/image:dev`), and containerd's CRI resolves references by
// exact name — `crictl images` lists the import under the normalised name,
// which is display only, while `crictl inspecti` and the kubelet report «no
// such image», so a pod with `imagePullPolicy: Never` fails with
// ErrImageNeverPull although the image is demonstrably there. Tagging the
// normalised name closes that. Verified with `crictl inspecti` at the end,
// because that is the lookup the kubelet performs.
//
// Requires a cluster created with the agent/images mount (k3c >= 0.2).
func ImageImport(cfg *config.Config, image string) error {
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("cluster '%s' is not running", cfg.Cluster)
	}
	if st, err := os.Stat(cfg.ImagesDir()); err != nil || !st.IsDir() {
		return fmt.Errorf("cluster '%s' has no images directory at %s: clusters created with an older k3c lack the images mount (recreate the cluster)",
			cfg.Cluster, cfg.ImagesDir())
	}
	logger.Info("exporting " + image + " from the host image store")
	name := fmt.Sprintf("k3c-import-%d.tar", time.Now().UnixNano())
	tar := filepath.Join(cfg.ImagesDir(), name)
	if out, err := runContainer("image", "save", image, "--output", tar); err != nil {
		return fmt.Errorf("image save failed: %s", out)
	}
	defer os.Remove(tar)

	qualified := normalizeImageRef(image)
	logger.Info("importing into the node's containerd as " + qualified)
	script := fmt.Sprintf("set -e; ctr -n k8s.io images import %s", shellQuote(nodeImagesDir+"/"+name))
	if qualified != image {
		script += fmt.Sprintf("; ctr -n k8s.io images tag --force %s %s", shellQuote(image), shellQuote(qualified))
	}
	if out, err := runContainer("exec", cfg.ServerName, "sh", "-c", script); err != nil {
		return fmt.Errorf("importing into the node failed: %s", strings.TrimSpace(out))
	}
	// The kubelet's own lookup, not the node's image list: node.status.images
	// lags and lists images the CRI cannot resolve by reference.
	if out, err := runContainer("exec", cfg.ServerName, "crictl", "inspecti", qualified); err != nil {
		return fmt.Errorf("imported, but the CRI does not resolve %s: %s", qualified, strings.TrimSpace(out))
	}
	logger.Info("image imported: " + qualified)
	return nil
}

// normalizeImageRef expands a reference the way the kubelet and containerd's
// CRI do before looking it up: a missing registry becomes docker.io, a bare
// name under docker.io gets the `library/` repository, and a reference with
// neither tag nor digest gets `:latest`. A digest is left alone.
func normalizeImageRef(ref string) string {
	name, rest := ref, ""
	if i := strings.Index(ref, "@"); i >= 0 {
		name, rest = ref[:i], ref[i:]
	} else if i := strings.LastIndex(ref, ":"); i >= 0 && !strings.Contains(ref[i:], "/") {
		name, rest = ref[:i], ref[i:]
	}
	parts := strings.Split(name, "/")
	first := parts[0]
	isRegistry := len(parts) > 1 && (strings.ContainsAny(first, ".:") || first == "localhost")
	if !isRegistry {
		if len(parts) == 1 {
			parts = append([]string{"docker.io", "library"}, parts...)
		} else {
			parts = append([]string{"docker.io"}, parts...)
		}
	}
	if rest == "" {
		rest = ":latest"
	}
	return strings.Join(parts, "/") + rest
}

// shellQuote makes s safe as one word in an `sh -c` script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
