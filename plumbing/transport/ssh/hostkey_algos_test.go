package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lunarhue/metallic-flock-go-git/v5/plumbing/transport"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// twoHostKeyServer stands up an sshd offering BOTH an RSA and an Ed25519 host
// key — the shape a stock OpenSSH server has — and returns its address plus the
// Ed25519 public key, which is the only one the client will pin.
func twoHostKeyServer(t *testing.T) (addr string, edPub ssh.PublicKey) {
	t.Helper()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPriv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(rsaSigner)
	cfg.AddHostKey(edSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, chReqs, err := nc.Accept()
					if err != nil {
						return
					}
					go ssh.DiscardRequests(chReqs)
					_ = ch.Close()
				}
			}()
		}
	}()

	return ln.Addr().String(), edSigner.PublicKey()
}

// TestHostKeyAlgorithmsFollowKnownHosts is a regression test for a silent
// interoperability failure: the auth method builds a known_hosts callback but
// leaves HostKeyAlgorithms empty, so the client advertises Go's default
// preference, which ranks RSA above Ed25519. A server offering both then
// presents its RSA host key while known_hosts pins only Ed25519, and
// verification fails with "knownhosts: key mismatch" against an entry that is
// perfectly current. OpenSSH does not have this failure because it orders host
// key algorithms by what it already knows for the host.
func TestHostKeyAlgorithmsFollowKnownHosts(t *testing.T) {
	addr, edPub := twoHostKeyServer(t)

	khPath := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("%s %s", knownhosts.Normalize(addr), ssh.MarshalAuthorizedKey(edPub))
	if err := os.WriteFile(khPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", khPath)

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	auth := &PublicKeysCallback{
		User:     "git",
		Callback: func() ([]ssh.Signer, error) { return nil, nil },
	}
	cmd := &command{
		endpoint: &transport.Endpoint{Protocol: "ssh", User: "git", Host: host, Port: port, Path: "/repo.git"},
		auth:     auth,
	}

	if err := cmd.connect(); err != nil {
		t.Fatalf("connect against a server offering RSA+Ed25519 with an Ed25519-only known_hosts: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Close() })
}

// TestKnownHostsDBRetainedForAlgorithmDerivation pins the seam the fix depends
// on: the fallback must retain its database, because HostKeyAlgorithms can only
// be derived once the target host is known, which happens in connect.
func TestKnownHostsDBRetainedForAlgorithmDerivation(t *testing.T) {
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPriv)
	if err != nil {
		t.Fatal(err)
	}
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	line := fmt.Sprintf("example.com %s", ssh.MarshalAuthorizedKey(edSigner.PublicKey()))
	if err := os.WriteFile(khPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS", khPath)

	auth := &PublicKeysCallback{User: "git", Callback: func() ([]ssh.Signer, error) { return nil, nil }}
	cfg, err := auth.ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HostKeyCallback == nil {
		t.Fatal("fallback did not set a HostKeyCallback")
	}
	db := auth.KnownHostsDB()
	if db == nil {
		t.Fatal("fallback did not retain its known_hosts database; connect cannot derive HostKeyAlgorithms")
	}
	algos := db.HostKeyAlgorithms("example.com:22")
	if len(algos) != 1 || algos[0] != ssh.KeyAlgoED25519 {
		t.Fatalf("HostKeyAlgorithms = %v, want [%s]", algos, ssh.KeyAlgoED25519)
	}
}
