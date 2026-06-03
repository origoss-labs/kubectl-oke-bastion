// Package tunnel opens the in-process SSH local port-forward through an ACTIVE
// OCI Bastion session: localhost:<local-port> → Bastion → <private
// endpoint>:6443 (ADR-0002, no system ssh binary). It is deliberately not
// unit-tested — a real forward needs a live SSH endpoint — and is verified by
// the slice's real-cluster smoke test.
package tunnel

import (
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialTimeout bounds the initial SSH handshake to the bastion host.
const dialTimeout = 30 * time.Second

// Params is everything Open needs to bring up the forward.
type Params struct {
	// BastionHost is the bastion SSH endpoint, host:port (port 22).
	BastionHost string
	// User is the SSH user — the session OCID.
	User string
	// Signer is the ephemeral key authorized for the session.
	Signer ssh.Signer
	// Target is the private resource the forward reaches, host:port
	// (<private endpoint>:6443).
	Target string
	// LocalPort is the loopback port to listen on; 0 lets the OS assign one.
	LocalPort int
}

// Tunnel is a running local port-forward. Close stops accepting, drops live
// connections, and closes the SSH transport.
type Tunnel struct {
	// LocalPort is the actual loopback port accepted on (resolved if LocalPort
	// was 0).
	LocalPort int

	listener net.Listener
	client   *ssh.Client
}

// Open dials the bastion, starts a loopback listener, and forwards every
// accepted connection through the session to Target. It returns once the
// listener is up; forwarding runs in the background until Close.
func Open(p Params) (*Tunnel, error) {
	cfg := &ssh.ClientConfig{
		User:    p.User,
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(p.Signer)},
		Timeout: dialTimeout,
		// The bastion host key is not known ahead of time and OCI does not
		// publish it. Accepting it is safe here: kubectl's traffic is verified
		// end-to-end by TLS against the cluster CA (tls-server-name, ADR-0005),
		// so a forged SSH transport cannot impersonate the API server.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", p.BastionHost, cfg)
	if err != nil {
		return nil, fmt.Errorf("dialing bastion %s: %w", p.BastionHost, err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", p.LocalPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}

	t := &Tunnel{
		LocalPort: ln.Addr().(*net.TCPAddr).Port,
		listener:  ln,
		client:    client,
	}
	go t.serve(p.Target)
	return t, nil
}

// serve accepts loopback connections and forwards each through the session.
func (t *Tunnel) serve(target string) {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			// Listener closed by Close, or a fatal accept error; stop serving.
			return
		}
		go t.forward(local, target)
	}
}

// forward pipes one accepted connection to target through the SSH session.
func (t *Tunnel) forward(local net.Conn, target string) {
	defer func() { _ = local.Close() }()
	remote, err := t.client.Dial("tcp", target)
	if err != nil {
		return
	}
	defer func() { _ = remote.Close() }()

	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(remote, local)
	go cp(local, remote)
	<-done
}

// Close stops accepting new connections and closes the SSH transport, which
// drops any in-flight forwarded connections.
func (t *Tunnel) Close() error {
	lerr := t.listener.Close()
	cerr := t.client.Close()
	if lerr != nil {
		return lerr
	}
	return cerr
}
