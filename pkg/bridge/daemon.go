package bridge

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

const DaemonArg = "bridge-serve"

// EnsureDaemon serializes startup across CLI sessions and waits for authenticated readiness.
func EnsureDaemon(paths *config.Paths) (int, string, error) {
	if err := config.EnsureDirs(paths); err != nil {
		return 0, "", err
	}
	startup := NewLock(filepath.Join(paths.DataDir, "bridge-start.lock"))
	deadline := time.Now().Add(10 * time.Second)
	for {
		locked, err := startup.TryLock()
		if err != nil {
			return 0, "", err
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("timed out waiting for bridge startup lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer startup.Unlock()
	if st, err := LoadState(paths); err == nil && Ping(st.Port, st.Token, 200*time.Millisecond) {
		return st.Port, st.Token, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	logPath := paths.BridgeLog
	if logPath == "" {
		logPath = filepath.Join(paths.DataDir, "bridge.log")
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, "", err
	}
	defer log.Close()
	dataDir, err := filepath.Abs(paths.DataDir)
	if err != nil {
		return 0, "", err
	}
	cmd := exec.Command(exe, DaemonArg, dataDir)
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := LoadState(paths); err == nil && Ping(st.Port, st.Token, 200*time.Millisecond) {
			return st.Port, st.Token, nil
		}
		select {
		case err := <-done:
			return 0, "", fmt.Errorf("bridge exited before becoming ready (%v); see %s", err, logPath)
		case <-time.After(25 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return 0, "", fmt.Errorf("bridge did not become ready; see %s", logPath)
}

func RunDaemon(paths *config.Paths) error {
	upstream, err := proxy.NewStore(paths).Active()
	if err != nil {
		return err
	}
	if upstream == nil || (upstream.Protocol != "socks5" && upstream.Protocol != "socks5h") {
		return fmt.Errorf("no active SOCKS proxy configured")
	}
	srv, err := NewServer(paths, upstream, 0)
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// StopDaemon never signals a stored PID, which can belong to a CLI or be reused.
func StopDaemon(paths *config.Paths) error {
	st, err := LoadState(paths)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !Ping(st.Port, st.Token, 200*time.Millisecond) {
		return nil
	}
	if !control(st.Port, st.Token, "POST", "/stop", time.Second) {
		return fmt.Errorf("bridge rejected the stop request")
	}
	for i := 0; i < 40; i++ {
		if !Ping(st.Port, st.Token, 100*time.Millisecond) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("bridge did not stop; restart the session that owns the legacy bridge")
}
