package cluster

import (
	"net"
	"testing"
)

// setVmnet pins the resolved default network for a test.
func setVmnet(t *testing.T, gateway, cidr string) {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	vmnetState.Lock()
	prevGw, prevSubnet := vmnetState.gateway, vmnetState.subnet
	vmnetState.gateway, vmnetState.subnet = gateway, subnet
	vmnetState.Unlock()
	t.Cleanup(func() {
		vmnetState.Lock()
		vmnetState.gateway, vmnetState.subnet = prevGw, prevSubnet
		vmnetState.Unlock()
	})
}

func TestAllowedSourceFollowsVmnet(t *testing.T) {
	setVmnet(t, "192.168.65.1", "192.168.65.0/24")
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"192.168.65.7:5000", true},
		{"[::ffff:192.168.65.7]:5000", true}, // IPv4-mapped from a dual-stack listener
		{"127.0.0.1:5000", true},
		{"192.168.64.7:5000", false}, // the usual range is not the resolved one
		{"192.168.1.20:5000", false}, // a LAN peer
	} {
		addr, err := net.ResolveTCPAddr("tcp", tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := allowedSource(addr); got != tc.want {
			t.Errorf("allowedSource(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestHostReachableIPFollowsVmnet(t *testing.T) {
	setVmnet(t, "192.168.65.1", "192.168.65.0/24")
	if got := hostReachableIP("192.168.131.3/24,192.168.65.6/24"); got != "192.168.65.6" {
		t.Errorf("got %s, want the vmnet address 192.168.65.6", got)
	}
}
