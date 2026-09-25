package runtime

import (
	"encoding/json"
	"fmt"
	"net"
)

// DefaultNetwork returns the IPv4 gateway and subnet of the runtime's builtin
// "default" (vmnet) network. vmnet picks the subnet when the system starts —
// usually 192.168.64.0/24, but the next free range (e.g. 192.168.65.0/24) when
// that one is unavailable — so callers must not assume a fixed range.
func DefaultNetwork() (gateway string, subnet *net.IPNet, err error) {
	out, err := Output("network", "inspect", "default")
	if err != nil {
		return "", nil, fmt.Errorf("inspecting the default network: %s", out)
	}
	return parseDefaultNetwork(out)
}

func parseDefaultNetwork(out string) (string, *net.IPNet, error) {
	var nets []struct {
		Status struct {
			IPv4Gateway string `json:"ipv4Gateway"`
			IPv4Subnet  string `json:"ipv4Subnet"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &nets); err != nil || len(nets) == 0 {
		return "", nil, fmt.Errorf("unexpected default network description: %s", out)
	}
	st := nets[0].Status
	_, subnet, err := net.ParseCIDR(st.IPv4Subnet)
	if err != nil {
		return "", nil, fmt.Errorf("default network subnet %q: %w", st.IPv4Subnet, err)
	}
	if gw := net.ParseIP(st.IPv4Gateway); gw == nil || !subnet.Contains(gw) {
		return "", nil, fmt.Errorf("default network gateway %q is not in %s", st.IPv4Gateway, subnet)
	}
	return st.IPv4Gateway, subnet, nil
}
