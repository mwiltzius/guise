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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"guise/internal/guisefs"
	"guise/internal/nfsmount"
	"guise/internal/registry"
	"guise/internal/vault"
)

func cmdMount(args []string) error {
	if len(args) == 0 {
		return errors.New("expected a mount command: start, stop, status")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("mount "+sub, flag.ContinueOnError)
	foreground := fs.Bool("foreground", false, "stay in the foreground")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	p, err := statePaths()
	if err != nil {
		return err
	}
	switch sub {
	case "start":
		return mountStart(p, *foreground)
	case "stop":
		return mountStop(p)
	case "status":
		return mountStatus(p)
	case "serve": // internal: the background process
		return mountServe(p, os.Stdin)
	}
	return fmt.Errorf("unknown mount command %q", sub)
}

// daemonPID returns the running daemon's PID, or 0.
func daemonPID(p paths) int {
	b, err := os.ReadFile(p.pidFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || syscall.Kill(pid, 0) != nil {
		return 0
	}
	return pid
}

// unlockVaults asks for the passphrase of every encrypted vault used by a
// guise, verifying each one.
func unlockVaults(p paths) (map[string]string, error) {
	r, err := registry.Load(p.config)
	if err != nil {
		return nil, err
	}
	pass := map[string]string{}
	for _, g := range r.Guises {
		for _, vp := range g.Vaults {
			if _, done := pass[vp]; done {
				continue
			}
			enc, err := vault.IsEncrypted(vp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v (guise %s will be unavailable)\n", err, g.Path)
				continue
			}
			if !enc {
				continue
			}
			for attempt := 0; ; attempt++ {
				b, err := readSecret("Passphrase for " + vp + ": ")
				if err != nil {
					return nil, err
				}
				if _, err := vault.Load(vp, func() ([]byte, error) { return b, nil }); err == nil {
					pass[vp] = string(b)
					break
				} else if attempt == 2 {
					return nil, err
				}
				fmt.Fprintln(os.Stderr, "Wrong passphrase, try again.")
			}
		}
	}
	return pass, nil
}

func mountStart(p paths, foreground bool) error {
	if pid := daemonPID(p); pid != 0 {
		fmt.Printf("Already mounted at %s (pid %d).\n", p.mount, pid)
		return nil
	}
	pass, err := unlockVaults(p)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(pass)
	if foreground {
		return mountServe(p, strings.NewReader(string(data)))
	}

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
	cmd := exec.Command(exe, "mount", "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	in.Write(data) // passphrases go over a pipe, never argv or env
	in.Close()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("mount failed (%v); see %s", err, p.logFile())
		case <-deadline:
			return fmt.Errorf("mount did not come up in time; see %s", p.logFile())
		case <-time.After(100 * time.Millisecond):
			if nfsmount.Mounted(p.mount) && daemonPID(p) != 0 {
				fmt.Printf("Guises mounted at %s.\n", p.mount)
				return nil
			}
		}
	}
}

func mountServe(p paths, in io.Reader) error {
	log.SetFlags(log.LstdFlags)
	var pass map[string]string
	if err := json.NewDecoder(in).Decode(&pass); err != nil {
		return fmt.Errorf("reading passphrases: %w", err)
	}
	for _, dir := range []string{p.config, p.mount, p.scratch} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := nfsmount.Unmount(p.mount); err != nil { // stale mount from a crash
		return err
	}
	fsys, err := guisefs.New(guisefs.Options{
		ConfigDir:  p.config,
		ScratchDir: p.scratch,
		PassFor: func(path string) vault.PassphraseFunc {
			return func() ([]byte, error) {
				if v, ok := pass[path]; ok {
					return []byte(v), nil
				}
				return nil, fmt.Errorf("vault %s is locked; restart with `guise mount stop && guise mount start`", path)
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
	if err := os.WriteFile(p.pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		srv.Stop()
		fsys.Close()
		return err
	}
	log.Printf("guise %s: mounted at %s", version, p.mount)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("received %v, unmounting", s)
	case err := <-srv.Done():
		log.Printf("server stopped: %v", err)
	}
	stopErr := srv.Stop()
	closeErr := fsys.Close()
	os.Remove(p.pidFile())
	if stopErr != nil {
		return stopErr
	}
	return closeErr
}

func mountStop(p paths) error {
	pid := daemonPID(p)
	if pid == 0 {
		if nfsmount.Mounted(p.mount) {
			return nfsmount.Unmount(p.mount)
		}
		fmt.Println("Not mounted.")
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 100; i++ {
		if daemonPID(p) == 0 {
			fmt.Println("Unmounted.")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon (pid %d) did not stop; see %s", pid, p.logFile())
}

func mountStatus(p paths) error {
	pid := daemonPID(p)
	mounted := nfsmount.Mounted(p.mount)
	switch {
	case pid != 0 && mounted:
		fmt.Printf("Mounted at %s (pid %d).\n", p.mount, pid)
	case pid != 0:
		fmt.Printf("Daemon running (pid %d) but %s is not mounted; see %s\n", pid, p.mount, p.logFile())
	case mounted:
		fmt.Printf("%s is mounted but no daemon is running; run `guise mount stop`.\n", p.mount)
	default:
		fmt.Println("Not mounted.")
	}
	r, err := registry.Load(p.config)
	if err == nil {
		fmt.Printf("%d guise(s) registered; log: %s\n", len(r.Guises), filepath.Clean(p.logFile()))
	}
	return nil
}
