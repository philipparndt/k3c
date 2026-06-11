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

// ImageImport loads an image from the host's `container` image store into
// the cluster under its original name — no registry involved. The image is
// saved as a tarball into the bind-mounted k3s agent/images directory,
// which k3s watches and imports automatically.
//
// Requires a cluster created with the agent/images mount (k3c >= 0.2).
func ImageImport(cfg *config.Config, image string) error {
	if !containerExists(cfg.ServerName, true) {
		return fmt.Errorf("cluster '%s' is not running", cfg.Cluster)
	}
	logger.Info("exporting " + image + " from the host image store")
	tar := filepath.Join(cfg.ImagesDir(), fmt.Sprintf("k3c-import-%d.tar", time.Now().UnixNano()))
	if out, err := runContainer("image", "save", image, "--output", tar); err != nil {
		return fmt.Errorf("image save failed: %s", out)
	}
	defer os.Remove(tar)

	logger.Info("waiting for the node to import the image")
	for i := 0; i < 30; i++ {
		out, err := kubectl(cfg, "get", "node", cfg.ServerName,
			"-o", "jsonpath={.status.images[*].names[*]}")
		if err == nil && strings.Contains(out, image) {
			logger.Info("image imported: " + image)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("image did not appear on the node; clusters created with an older k3c lack the images mount (recreate the cluster)")
}
