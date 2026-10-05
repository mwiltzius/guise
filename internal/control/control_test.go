package control

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	sock, err := SocketPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var got Request
	go Serve(l, func(r Request) Response {
		got = r
		if r.Op == "boom" {
			return Response{Error: "nope"}
		}
		return Response{OK: true, PID: 42}
	})
	resp, err := Call(sock, Request{Op: OpUnlock, Passphrases: map[string]string{"/v.age": "pw"}})
	if err != nil || resp.PID != 42 || got.Passphrases["/v.age"] != "pw" {
		t.Fatalf("resp %+v, err %v, got %+v", resp, err, got)
	}
	if _, err := Call(sock, Request{Op: "boom"}); err == nil || err.Error() != "nope" {
		t.Fatalf("err = %v", err)
	}
	if _, err := Listen(sock); err == nil {
		t.Fatal("second listener allowed while the first is running")
	}
}

func TestLongSocketPathFallsBack(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	p, err := SocketPath(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > maxSocketPath || !strings.HasPrefix(p, "/tmp/guise-") {
		t.Fatalf("p = %q", p)
	}
	if q, _ := SocketPath(long); q != p {
		t.Fatal("fallback path not stable")
	}
}
