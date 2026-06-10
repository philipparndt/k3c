package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// The ignore-cpu-requests feature: a mutating admission webhook that strips
// CPU requests from pods at creation, so neither the scheduler nor the
// kubelet can reject workloads on a laptop whose chart requests assume
// production sizing. Kubernetes offers no switch for this; mutation is the
// only clean way.
//
// Because the cluster can reach the host at the vmnet gateway, the webhook
// runs inside the k3c host daemons (URL-mode MutatingWebhookConfiguration)
// — no in-cluster components, no cert-manager. failurePolicy is Ignore, so
// the cluster keeps working if the daemon is down (pods then simply keep
// their requests).

const webhookPort = "9443"

func webhookCertPath(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "webhook.crt")
}

func webhookKeyPath(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "webhook.key")
}

// ensureWebhookCert creates (or reuses) a self-signed certificate for the
// vmnet gateway IP, used by the admission webhook listener.
func ensureWebhookCert(cfg *config.Config) error {
	if _, err := os.Stat(webhookCertPath(cfg)); err == nil {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "k3c-webhook"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP(cfg.VmnetGateway)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDer, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(webhookCertPath(cfg),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(webhookKeyPath(cfg),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600)
}

// ignoredResources returns the request resource names to strip.
func ignoredResources(cfg *config.Config) []string {
	var resources []string
	if cfg.IgnoreCPURequests {
		resources = append(resources, "cpu")
	}
	if cfg.IgnoreMemoryRequests {
		resources = append(resources, "memory")
	}
	return resources
}

// negligible per-resource request values. Requests cannot simply be
// removed: containers with limits get requests re-defaulted to the limit
// values by the API machinery, so they are replaced instead.
var negligibleRequest = map[string]string{"cpu": "1m", "memory": "1Mi"}

// stripRequestsPatch builds JSONPatch operations replacing the given
// request resources with negligible values in all containers of the pod.
func stripRequestsPatch(pod map[string]any, resources []string) []map[string]any {
	var patch []map[string]any
	spec, _ := pod["spec"].(map[string]any)
	if spec == nil {
		return nil
	}
	for _, kind := range []string{"initContainers", "containers"} {
		containers, _ := spec[kind].([]any)
		for i, c := range containers {
			container, _ := c.(map[string]any)
			res, _ := container["resources"].(map[string]any)
			requests, _ := res["requests"].(map[string]any)
			for _, name := range resources {
				if value, ok := requests[name]; ok && value != negligibleRequest[name] {
					patch = append(patch, map[string]any{
						"op":    "replace",
						"path":  fmt.Sprintf("/spec/%s/%d/resources/requests/%s", kind, i, name),
						"value": negligibleRequest[name],
					})
				}
			}
		}
	}
	return patch
}

func handleMutatePods(w http.ResponseWriter, r *http.Request, resources []string) {
	logger.Debug("admission request from " + r.RemoteAddr)
	var review map[string]any
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request, _ := review["request"].(map[string]any)
	uid, _ := request["uid"].(string)
	response := map[string]any{"uid": uid, "allowed": true}
	if pod, ok := request["object"].(map[string]any); ok {
		if patch := stripRequestsPatch(pod, resources); len(patch) > 0 {
			data, err := json.Marshal(patch)
			if err == nil {
				response["patchType"] = "JSONPatch"
				response["patch"] = base64.StdEncoding.EncodeToString(data)
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"apiVersion": "admission.k8s.io/v1",
		"kind":       "AdmissionReview",
		"response":   response,
	})
}

// serveWebhook runs the admission webhook listener (TLS).
func serveWebhook(cfg *config.Config) error {
	if err := ensureWebhookCert(cfg); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(webhookCertPath(cfg), webhookKeyPath(cfg))
	if err != nil {
		return err
	}
	resources := ignoredResources(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/mutate-pods", func(w http.ResponseWriter, r *http.Request) {
		handleMutatePods(w, r, resources)
	})
	server := &http.Server{
		Addr:      "0.0.0.0:" + webhookPort,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}
	logger.Info("admission webhook listening on :" + webhookPort)
	return server.ListenAndServeTLS("", "")
}

// applyIgnoreCPUWebhook registers (or removes) the mutating webhook in the
// cluster, according to the ignoreCpuRequests config.
func applyIgnoreCPUWebhook(cfg *config.Config) error {
	if len(ignoredResources(cfg)) == 0 {
		_, _ = kubectl(cfg, "delete", "mutatingwebhookconfiguration", "k3c-ignore-cpu-requests", "--ignore-not-found")
		return nil
	}
	logger.Info("registering ignore-cpu-requests admission webhook")
	if err := ensureWebhookCert(cfg); err != nil {
		return err
	}
	certPem, err := os.ReadFile(webhookCertPath(cfg))
	if err != nil {
		return err
	}
	manifest := fmt.Sprintf(`apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: k3c-ignore-cpu-requests
webhooks:
  - name: ignore-cpu-requests.k3c.dev
    clientConfig:
      url: https://%s:%s/mutate-pods
      caBundle: %s
    rules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["pods"]
    namespaceSelector:
      matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: NotIn
          values: ["kube-system"]
    failurePolicy: Ignore
    sideEffects: None
    admissionReviewVersions: ["v1"]
    timeoutSeconds: 5
`, cfg.VmnetGateway, webhookPort, base64.StdEncoding.EncodeToString(certPem))
	apply := kubectlApplyStdin(cfg, manifest)
	if apply != nil {
		return fmt.Errorf("webhook registration failed: %w", apply)
	}
	return nil
}

func kubectlApplyStdin(cfg *config.Config, manifest string) error {
	cmd := kubectlCommand(cfg, "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", out)
	}
	return nil
}
