package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"guise/internal/control"
	"guise/internal/guisefs"
	"guise/internal/nfsmount"
	"guise/internal/registry"
	"guise/internal/vault"
)

// The background process serves guises. It is started automatically by
// `guise new` (or at login, see autostart); the commands here are only for
// inspecting and controlling it.

func (p paths) socket() (string, error) { return control.SocketPath(p.config) }

// status asks the background process for its status.
func status(p paths) (control.Response, error) {
	sock, err := p.socket()
	if err != nil {
		return control.Response{}, err
	}
	return control.Call(sock, control.Request{Op: control.OpStatus})
}

func send(p paths, req control.Request) (control.Response, error) {
	sock, err := p.socket()
	if err != nil {
		return control.Response{}, err
	}
	return control.Call(sock, req)
}

// known holds passphrases entered during this command, so nobody is asked
// twice for the same vault.
var known = map[string][]byte{}

// unlockVault returns a verified passphrase for an encrypted vault,
// prompting (up to three tries) if it is not already known.
func unlockVault(path string) ([]byte, error) {
	if pw, ok := known[path]; ok {
		return pw, nil
	}
	for attempt := 1; ; attempt++ {
		pw, err := readSecret("Passphrase for " + path + ": ")
		if err != nil {
			return nil, err
		}
		if _, err := vault.Load(path, func() ([]byte, error) { return pw, nil }); err == nil {
			known[path] = pw
			return pw, nil
		} else if attempt == 3 {
			return nil, err
		}
		fmt.Fprintln(os.Stderr, "Wrong passphrase, try again.")
	}
}

// encryptedVaults returns the encrypted vaults among paths, deduplicated.
func encryptedVaults(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if enc, _ := vault.IsEncrypted(p); enc {
			out = append(out, p)
		}
	}
	return out
}

func allVaults(p paths) ([]string, error) {
	r, err := registry.Load(p.config)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range r.Guises {
		out = append(out, g.Vaults...)
	}
	return out, nil
}

func collect(vaults []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range encryptedVaults(vaults) {
		pw, err := unlockVault(v)
		if err != nil {
			return nil, err
		}
		out[v] = string(pw)
	}
	return out, nil
}

// ensureServing makes sure the background process is running, has the
// given vaults unlocked, and has picked up the latest registry.
func ensureServing(p paths, vaults []string) error {
	if resp, err := status(p); err == nil {
		need := map[string]bool{}
		for _, v := range vaults {
			need[v] = true
		}
		var locked []string
		for _, v := range resp.Locked {
			if need[v] {
				locked = append(locked, v)
			}
		}
		if len(locked) > 0 {
			pass, err := collect(locked)
			if err != nil {
				return err
			}
			if _, err := send(p, control.Request{Op: control.OpUnlock, Passphrases: pass}); err != nil {
				return err
			}
		}
		_, err := send(p, control.Request{Op: control.OpReload})
		return err
	}
	pass, err := collect(vaults)
	if err != nil {
		return err
	}
	return spawn(p, pass)
}

// spawn starts the background process, handing it passphrases over a pipe
// (never argv or the environment), and waits until it is serving.
func spawn(p paths, pass map[string]string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.config, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(p.logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "_serve", "--stdin")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	data, _ := json.Marshal(pass)
	in.Write(data)
	in.Close()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("background process failed to start (%v); see %s", err, p.logFile())
		case <-deadline:
			return fmt.Errorf("background process did not start in time; see %s", p.logFile())
		case <-time.After(100 * time.Millisecond):
			if _, err := status(p); err == nil {
				return nil
			}
		}
	}
}

// --- commands ----------------------------------------------------------

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "stay in the foreground")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	if resp, err := status(p); err == nil {
		fmt.Printf("Already running (pid %d).\n", resp.PID)
		return nil
	}
	vaults, err := allVaults(p)
	if err != nil {
		return err
	}
	pass, err := collect(vaults)
	if err != nil {
		return err
	}
	if *foreground {
		data, _ := json.Marshal(pass)
		return serve(p, strings.NewReader(string(data)))
	}
	if err := spawn(p, pass); err != nil {
		return err
	}
	fmt.Println("Guises are being served.")
	return nil
}

func cmdStop(args []string) error {
	if _, err := parse(flag.NewFlagSet("stop", flag.ContinueOnError), args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	resp, err := send(p, control.Request{Op: control.OpStop})
	if err != nil {
		if nfsmount.Mounted(p.mount) { // left over from a crash
			return nfsmount.Unmount(p.mount)
		}
		fmt.Println("Not running.")
		return nil
	}
	for i := 0; i < 150; i++ {
		if syscall.Kill(resp.PID, 0) != nil { // process has exited
			fmt.Println("Stopped. Guises are unavailable until the next `guise` command that needs them, or `guise start`.")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("background process did not stop; see %s", p.logFile())
}

func cmdStatus(args []string) error {
	if _, err := parse(flag.NewFlagSet("status", flag.ContinueOnError), args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	resp, err := status(p)
	if err != nil {
		fmt.Println("Not running. It starts automatically with `guise new`, or run `guise start`.")
		return nil
	}
	fmt.Printf("Serving %d guise(s) (pid %d, mount %s).\n", resp.Guises, resp.PID, resp.Mount)
	for _, v := range resp.Locked {
		fmt.Printf("Locked vault: %s\n", v)
	}
	for _, g := range resp.Unavailable {
		fmt.Printf("Unavailable: %s\n", g)
	}
	if len(resp.Locked) > 0 {
		fmt.Println("Run `guise unlock` to unlock.")
	}
	fmt.Println("Log:", p.logFile())
	return nil
}

func cmdUnlock(args []string) error {
	if _, err := parse(flag.NewFlagSet("unlock", flag.ContinueOnError), args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	resp, err := status(p)
	if err != nil { // not running: start with everything unlocked
		vaults, err := allVaults(p)
		if err != nil {
			return err
		}
		pass, err := collect(vaults)
		if err != nil {
			return err
		}
		return spawn(p, pass)
	}
	if len(resp.Locked) == 0 {
		fmt.Println("All vaults are unlocked.")
		return nil
	}
	pass, err := collect(resp.Locked)
	if err != nil {
		return err
	}
	if _, err := send(p, control.Request{Op: control.OpUnlock, Passphrases: pass}); err != nil {
		return err
	}
	fmt.Printf("Unlocked %d vault(s).\n", len(pass))
	return nil
}

// --- the background process ----------------------------------------------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("_serve", flag.ContinueOnError)
	fromStdin := fs.Bool("stdin", false, "read passphrases from stdin")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	in := io.Reader(strings.NewReader("{}"))
	if *fromStdin {
		in = os.Stdin
	}
	return serve(p, in)
}

// keyring holds unlocked passphrases inside the background process.
type keyring struct {
	mu   sync.Mutex
	pass map[string]string
}

func (k *keyring) get(path string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.pass[path]
	return v, ok
}

func (k *keyring) add(path, pw string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pass[path] = pw
}

func serve(p paths, in io.Reader) error {
	log.SetFlags(log.LstdFlags)
	keys := &keyring{pass: map[string]string{}}
	if err := json.NewDecoder(in).Decode(&keys.pass); err != nil {
		return fmt.Errorf("reading passphrases: %w", err)
	}
	for _, dir := range []string{p.config, p.mount, p.scratch} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	sock, err := p.socket()
	if err != nil {
		return err
	}
	// Listen also guarantees a single instance. Closing the listener removes
	// the socket file; never remove it by path, or a stopping process could
	// delete the socket of one that has just started.
	l, err := control.Listen(sock)
	if err != nil {
		return err
	}
	defer l.Close()
	if err := nfsmount.Unmount(p.mount); err != nil { // stale mount from a crash
		return err
	}

	fsys, err := guisefs.New(guisefs.Options{
		ConfigDir:  p.config,
		ScratchDir: p.scratch,
		PassFor: func(path string) vault.PassphraseFunc {
			return func() ([]byte, error) {
				if v, ok := keys.get(path); ok {
					return []byte(v), nil
				}
				return nil, fmt.Errorf("vault %s is locked; run `guise unlock`", path)
			}
		},
		Logf: log.Printf,
	})
	if err != nil {
		return err
	}
	srv, err := nfsmount.Start(fsys, p.mount)
	if err != nil {
		fsys.Close()
		return err
	}
	log.Printf("guise %s: serving at %s", version, p.mount)

	locked := func() []string {
		vaults, err := allVaults(p)
		if err != nil {
			return nil
		}
		var out []string
		for _, v := range encryptedVaults(vaults) {
			if _, ok := keys.get(v); !ok {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return out
	}
	if l := locked(); len(l) > 0 {
		notify(fmt.Sprintf("%d vault(s) locked — some guises are unavailable. Run `guise unlock`.", len(l)))
	}

	stop := make(chan struct{})
	var stopOnce sync.Once
	go control.Serve(l, func(req control.Request) control.Response {
		switch req.Op {
		case control.OpStatus:
		case control.OpReload:
			if err := fsys.Reopen(); err != nil {
				return control.Response{Error: err.Error()}
			}
		case control.OpUnlock:
			for path, pw := range req.Passphrases {
				if _, err := vault.Load(path, func() ([]byte, error) { return []byte(pw), nil }); err != nil {
					return control.Response{Error: fmt.Sprintf("unlock %s: %v", path, err)}
				}
				keys.add(path, pw)
			}
			if err := fsys.Reopen(); err != nil {
				return control.Response{Error: err.Error()}
			}
		case control.OpStop:
			stopOnce.Do(func() { close(stop) })
		default:
			return control.Response{Error: "unknown operation " + req.Op}
		}
		return control.Response{OK: true, PID: os.Getpid(), Mount: p.mount, Locked: locked(),
			Unavailable: fsys.Unavailable(), Guises: fsys.Guises()}
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("received %v, stopping", s)
	case <-stop:
		log.Printf("stop requested")
	case err := <-srv.Done():
		log.Printf("server stopped: %v", err)
	}
	l.Close()
	stopErr := srv.Stop()
	closeErr := fsys.Close()
	return errors.Join(stopErr, closeErr)
}

// notify shows a desktop notification (macOS), best effort.
func notify(msg string) {
	script := fmt.Sprintf("display notification %q with title \"guise\"", msg)
	exec.Command("/usr/bin/osascript", "-e", script).Run()
}
