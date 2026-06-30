// Command k3cdockerfwd is the in-guest forwarder for the docker sidecar's
// nested published ports. It is cross-compiled for linux/arm64, shipped in the
// runtime bundle, injected into the docker:dind VM, and launched there with
// `container exec -d`. See package k3c/dockerfwd for the wire protocol.
package main

import (
	"flag"
	"log"

	"k3c/dockerfwd"
)

func main() {
	socket := flag.String("socket", "/run/k3c-docker-fwd.sock", "unix socket to listen on (bridged to the host via --publish-socket)")
	flag.Parse()
	if err := dockerfwd.Serve(*socket); err != nil {
		log.Fatalf("k3cdockerfwd: %v", err)
	}
}
