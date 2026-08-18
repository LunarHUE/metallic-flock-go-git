// Package client contains helper function to deal with the different client
// protocols.
package client

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/file"
	"github.com/go-git/go-git/v5/plumbing/transport/git"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// FileClient serves file:// endpoints from IN-PROCESS storage.
//
// It is registered as the "file" transport in place of file.DefaultClient
// because that client is not self-contained: it locates and executes the
// git-upload-pack and git-receive-pack binaries, and shells out to
// "git --exec-path" to find them. A program that reaches for go-git precisely to
// avoid depending on a git installation still acquires that dependency the
// moment it clones, fetches, or pushes a local path — and it discovers this at
// run time on the deployed host, not at build time.
//
// The in-process server speaks the same pack protocol against the repository's
// own storage, so local clone, fetch, and push behave as before with no external
// binary involved. file.DefaultClient stays exported for callers that want the
// exec-based behaviour back:
//
//	client.InstallProtocol("file", file.DefaultClient)
var FileClient = server.NewClient(server.DefaultLoader)

// ExecFileClient is the upstream file transport, which executes the git
// pack binaries. Named so the trade-off is greppable from a call site that
// deliberately reinstates it.
var ExecFileClient = file.DefaultClient

// Protocols are the protocols supported by default.
var Protocols = map[string]transport.Transport{
	"http":  http.DefaultClient,
	"https": http.DefaultClient,
	"ssh":   ssh.DefaultClient,
	"git":   git.DefaultClient,
	"file":  FileClient,
}

// InstallProtocol adds or modifies an existing protocol.
func InstallProtocol(scheme string, c transport.Transport) {
	if c == nil {
		delete(Protocols, scheme)
		return
	}

	Protocols[scheme] = c
}

// NewClient returns the appropriate client among of the set of known protocols:
// http://, https://, ssh:// and file://.
// See `InstallProtocol` to add or modify protocols.
func NewClient(endpoint *transport.Endpoint) (transport.Transport, error) {
	return getTransport(endpoint)
}

func getTransport(endpoint *transport.Endpoint) (transport.Transport, error) {
	f, ok := Protocols[endpoint.Protocol]
	if !ok {
		return nil, fmt.Errorf("unsupported scheme %q", endpoint.Protocol)
	}

	if f == nil {
		return nil, fmt.Errorf("malformed client for scheme %q, client is defined as nil", endpoint.Protocol)
	}
	return f, nil
}
