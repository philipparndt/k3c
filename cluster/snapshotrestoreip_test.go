package cluster

import "testing"

// containerHolding decides whether a warm restore can reclaim the
// snapshot-time server IP: only a RUNNING container on that address blocks
// the reclaim (the cluster's own containers are stopped before the check).
func TestContainerHolding(t *testing.T) {
	// container ls output: running containers only, IP column may carry
	// several comma-separated CIDRs (vmnet + gvnet NICs).
	ls := `ID              IMAGE                               OS     ARCH   STATE    ADDR                               CPUS  MEMORY    CREATED
vehub-registry  docker.io/library/registry:2        linux  arm64  running  192.168.64.3/24                    4     1024 MB   2026-07-02T11:49:29Z
k3c-docker      docker.io/library/docker:dind       linux  arm64  running  192.168.64.8/24,192.168.127.2/24   10    49152 MB  2026-07-02T07:57:28Z
vehub-server    docker.io/rancher/k3s:v1.36.1-k3s1  linux  arm64  running  192.168.64.4/24,192.168.129.2/24   6     32768 MB  2026-07-02T11:49:39Z`

	cases := []struct {
		ip   string
		want string
	}{
		{"192.168.64.3", "vehub-registry"},  // the reported swap: registry took the server's old IP
		{"192.168.127.2", "k3c-docker"},     // second NIC in the comma list
		{"192.168.64.5", ""},                // free address
		{"192.168.64.30", ""},               // prefix of .3 must not match .30 (and vice versa)
		{"192.168.64", ""},                  // partial address must not match
	}
	for _, c := range cases {
		if got := containerHolding(ls, c.ip); got != c.want {
			t.Errorf("containerHolding(%q) = %q, want %q", c.ip, got, c.want)
		}
	}

	if got := containerHolding("", "192.168.64.3"); got != "" {
		t.Errorf("empty ls output should hold nothing, got %q", got)
	}
}
