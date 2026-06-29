// Package dockerfwd is the in-guest forwarder for the docker sidecar's nested
// published ports. It runs INSIDE the docker:dind VM (linux/arm64) and is
// reached from the host over a single unix socket that the Apple `container`
// runtime bridges to the host via `--publish-socket` — so the host never has to
// dial the guest's vmnet IP, which is not host-reachable at L2 (see the
// docker-sidecar-host-forwarder change, OQ#1/OQ#2).
//
// Wire protocol (host → forwarder), one connection per forwarded TCP stream:
//
//	"<port>\n"   ASCII decimal target port, then the connection becomes a
//	             transparent byte pipe.
//
// The forwarder dials 127.0.0.1:<port> inside the VM (dockerd publishes nested
// container ports on 0.0.0.0:<port>, reachable on loopback) and splices.
package dockerfwd

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	dialTimeout = 10 * time.Second
	// headerTimeout bounds how long a connection may take to send its one-line
	// port header before we give up — so a stuck/half-open client can't pin a
	// goroutine forever. Cleared before splicing the stream.
	headerTimeout = 30 * time.Second
)

// WriteHeader writes the wire header selecting the target port. The host side
// calls this right after connecting, before splicing the client stream.
func WriteHeader(w io.Writer, port int) error {
	_, err := fmt.Fprintf(w, "%d\n", port)
	return err
}

// Serve listens on socketPath (a unix socket) and forwards each connection to
// 127.0.0.1:<port> inside the guest, where <port> is read from the wire header.
// A stale socket file is removed first. Serve blocks until the listener fails.
func Serve(socketPath string) error {
	_ = os.Remove(socketPath) // a stale socket from a prior run blocks bind
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(conn)
	}
}

func handle(conn net.Conn) {
	defer conn.Close()
	// bufio so any payload bytes that arrive in the same read as the header
	// are not lost; the buffered reader is the source for the host→guest pump.
	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(headerTimeout))
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // clear: the stream itself has no deadline
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || port <= 0 || port > 65535 {
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), dialTimeout)
	if err != nil {
		return
	}
	defer upstream.Close()
	splice(conn, br, upstream)
}

// splice copies bidirectionally between the client connection (write side
// `conn`, read side `cr`, which may hold buffered bytes) and `upstream`,
// half-closing each write side on EOF so half-duplex streams (e.g. `docker run`
// without a TTY) are not torn down early — mirroring cluster.splice.
func splice(conn net.Conn, cr io.Reader, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, cr)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		closeWrite(conn)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
