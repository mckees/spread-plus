package spread_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	. "gopkg.in/check.v1"

	"github.com/canonical/spread-plus/spread"
)

type clientSuite struct{}

var _ = Suite(&clientSuite{})

func (s *clientSuite) TestDialOnReboot(c *C) {
	restore := spread.MockSshDial(func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
		time.Sleep(1 * time.Second)
		return nil, fmt.Errorf("cannot connect")
	})
	defer restore()

	cli := spread.MockClient()
	spread.SetWarnTimeout(cli, 50*time.Millisecond)
	spread.SetKillTimeout(cli, 100*time.Millisecond)

	err := spread.DialOnReboot(cli, "")
	c.Check(err, ErrorMatches, "kill-timeout reached after mock-job reboot request")
}

// sshPeer starts an SSH server on loopback and returns a connected client.
// When reply is false the server never answers global requests, like a
// device that vanished without closing the TCP stream.
func sshPeer(c *C, reply bool) (*ssh.Client, func()) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	c.Assert(err, IsNil)
	signer, err := ssh.NewSignerFromKey(key)
	c.Assert(err, IsNil)
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		go func() {
			for ch := range chans {
				ch.Reject(ssh.Prohibited, "")
			}
		}()
		if reply {
			ssh.DiscardRequests(reqs)
		} else {
			for range reqs {
			}
		}
	}()

	sshc, err := ssh.Dial("tcp", l.Addr().String(), &ssh.ClientConfig{
		User:            "mock",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	c.Assert(err, IsNil)
	return sshc, func() { sshc.Close(); l.Close() }
}

func (s *clientSuite) TestKeepAliveClosesUnresponsivePeer(c *C) {
	restore := spread.MockKeepAlive(10*time.Millisecond, 100*time.Millisecond)
	defer restore()
	sshc, cleanup := sshPeer(c, false)
	defer cleanup()

	go spread.KeepAlive(sshc, "peer")

	closed := make(chan error, 1)
	go func() { closed <- sshc.Wait() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		c.Fatalf("keepalive did not close connection to unresponsive peer")
	}
}

func (s *clientSuite) TestKeepAliveKeepsResponsivePeer(c *C) {
	restore := spread.MockKeepAlive(10*time.Millisecond, 100*time.Millisecond)
	defer restore()
	sshc, cleanup := sshPeer(c, true)
	defer cleanup()

	done := make(chan struct{})
	go func() { spread.KeepAlive(sshc, "peer"); close(done) }()

	closed := make(chan error, 1)
	go func() { closed <- sshc.Wait() }()
	select {
	case <-closed:
		c.Fatalf("keepalive closed connection to responsive peer")
	case <-time.After(500 * time.Millisecond):
	}

	sshc.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatalf("keepalive did not stop after connection closed")
	}
}
