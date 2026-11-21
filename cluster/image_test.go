package cluster

import "testing"

// The kubelet resolves images by their normalised reference; an import that
// keeps the host store's short name is listed but never found (ErrImageNeverPull).
func TestNormalizeImageRef(t *testing.T) {
	cases := map[string]string{
		"my-app:dev":                          "docker.io/library/my-app:dev",
		"my-app":                              "docker.io/library/my-app:latest",
		"team/my-app:1.2":                     "docker.io/team/my-app:1.2",
		"docker.io/library/my-app:dev":        "docker.io/library/my-app:dev",
		"localhost:5001/my/image:dev":         "localhost:5001/my/image:dev",
		"registry.example.com/my/image":       "registry.example.com/my/image:latest",
		"registry.example.com:5000/img:v1":    "registry.example.com:5000/img:v1",
		"my-app@sha256:0123456789abcdef":      "docker.io/library/my-app@sha256:0123456789abcdef",
		"ghcr.io/org/img@sha256:0123456789ab": "ghcr.io/org/img@sha256:0123456789ab",
	}
	for in, want := range cases {
		if got := normalizeImageRef(in); got != want {
			t.Errorf("normalizeImageRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}
