package cluster

import (
	"net"
	"sync"

	"k3c/config"
	"k3c/runtime"

	"github.com/philipparndt/go-logger"
)

// The runtime's default (vmnet) network. vmnet assigns its subnet when the
// container system starts — usually 192.168.64.0/24, but the next free range
// when that one is unavailable — so it is read from the running system rather
// than assumed. Cached once resolved: it only changes with a system restart.
var vmnetState struct {
	sync.Mutex
	gateway string
	subnet  *net.IPNet
}

// vmnet returns the default network's gateway and subnet, inspecting the
// running system on first use. When it cannot be inspected (system down) it
// returns the usual range without caching it, so a later call retries.
func vmnet() (string, *net.IPNet) {
	vmnetState.Lock()
	defer vmnetState.Unlock()
	if vmnetState.subnet == nil {
		gw, subnet, err := runtime.DefaultNetwork()
		if err != nil {
			logger.Debug("using the default vmnet range: " + err.Error())
			return defaultVmnet()
		}
		if subnet.String() != config.DefaultVmnetSubnet {
			logger.Info("container default network is " + subnet.String() +
				" (not the usual " + config.DefaultVmnetSubnet + ")")
		}
		vmnetState.gateway, vmnetState.subnet = gw, subnet
	}
	return vmnetState.gateway, vmnetState.subnet
}

// vmnetCached is vmnet without inspecting the system: the resolved network,
// or the usual range when it has not been resolved. For hot paths (per
// connection in the daemons) that must not invoke the container CLI.
func vmnetCached() (string, *net.IPNet) {
	vmnetState.Lock()
	defer vmnetState.Unlock()
	if vmnetState.subnet == nil {
		return defaultVmnet()
	}
	return vmnetState.gateway, vmnetState.subnet
}

func defaultVmnet() (string, *net.IPNet) {
	_, subnet, _ := net.ParseCIDR(config.DefaultVmnetSubnet)
	return config.DefaultVmnetGateway, subnet
}

// ResolveVmnet points cfg at the running system's default network, so the
// gateway services (proxy, pull cache, registry, webhook) and the guest
// scripts use the addresses vmnet actually assigned.
func ResolveVmnet(cfg *config.Config) {
	gw, subnet := vmnet()
	cfg.VmnetGateway, cfg.VmnetSubnet = gw, subnet.String()
}

// onVmnet reports whether ip is on the default network.
func onVmnet(ip string, subnet *net.IPNet) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && subnet.Contains(parsed)
}
