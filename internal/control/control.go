// Package control is the private channel between the guise CLI and the
// background process: a Unix socket, readable only by the user, speaking one
// JSON request and one JSON response per connection.
//
// No operation returns vault contents. A process that can reach the socket
// can at most ask for status, trigger a reload, supply passphrases, or stop
// the background process.
package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Operations.
const (
	OpStatus = "status"
	OpReload = "reload" // re-read the registry now
	OpUnlock = "unlock" // supply vault passphrases
	OpStop   = "stop"
)

// Request is sent by the CLI.
type Request struct {
	Op          string            `json:"op"`
	Passphrases map[string]string `json:"passphrases,omitempty"`
}

// Response is sent by the background process.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	PID   int    `json:"pid,omitempty"`
	Mount string `json:"mount,omitempty"`
	// Locked lists encrypted vaults used by guises that are not unlocked.
	Locked []string `json:"locked,omitempty"`
	// Unavailable lists guise paths that cannot be served right now.
	Unavailable []string `json:"unavailable,omitempty"`
	Guises      int      `json:"guises"`
}

// maxSocketPath is below the 104-byte sun_path limit on macOS.
const maxSocketPath = 100

// SocketPath returns the socket location for a state directory, falling back
// to a short per-user path under /tmp when the natural one is too long for a
// Unix socket.
func SocketPath(stateDir string) (string, error) {
	p := filepath.Join(stateDir, "guise.sock")
	if len(p) <= maxSocketPath {
		return p, nil
	}
	sum := sha256.Sum256([]byte(stateDir))
	dir := filepath.Join("/tmp", fmt.Sprintf("guise-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ownedByMe(fi) {
		return "", fmt.Errorf("%s is not a private directory owned by you", dir)
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock"), nil
}

// Listen creates the socket, replacing a stale one, with owner-only access.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := Call(path, Request{Op: OpStatus}); err == nil {
		return nil, errors.New("another guise process is already running")
	}
	os.Remove(path)
	old := umask(0o077)
	l, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve answers requests until the listener is closed.
func Serve(l net.Listener, handle func(Request) Response) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			if !peerIsMe(conn) {
				return
			}
			var req Request
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				return
			}
			json.NewEncoder(conn).Encode(handle(req))
		}()
	}
}

// Call sends one request. It fails quickly if nothing is listening.
func Call(path string, req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
