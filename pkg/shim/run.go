package shim

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func EnsureBridge(paths *config.Paths, _ *proxy.Config) (int, string, error) {
	return bridge.EnsureDaemon(paths)
}

func BuildProxyEnv(paths *config.Paths, store *proxy.Store) ([]string, error) {
	active, err := store.Active()
	if err != nil {
		return nil, err
	}
	if active == nil {
		return nil, nil
	}

	// Expiry check
	if active.ExpiresAt > 0 && time.Now().Unix() >= active.ExpiresAt {
		if active.Fallback {
			fmt.Fprintln(os.Stderr, "agy-switch: proxy expired — falling back to DIRECT connection")
			return nil, nil
		}
		return nil, fmt.Errorf("proxy %q has expired and fallback is disabled", active.Name)
	}

	proto := strings.ToLower(active.Protocol)
	var proxyURL string

	if proto == "socks5" || proto == "socks5h" {
		port, token, bErr := EnsureBridge(paths, active)
		if bErr == nil && port > 0 && token != "" {
			proxyURL = fmt.Sprintf("http://%s:%s@127.0.0.1:%d", bridge.BridgeUser, token, port)
		} else {
			return nil, fmt.Errorf("start SOCKS bridge: %w", bErr)
		}
	} else {
		proxyURL = active.URL()
	}

	if proxyURL == "" {
		return nil, nil
	}

	noProxy := "localhost,127.0.0.1,::1"

	return []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"ALL_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"all_proxy=" + proxyURL,
		"NO_PROXY=" + noProxy,
		"no_proxy=" + noProxy,
	}, nil
}

func ExecuteRealAgy(paths *config.Paths, args []string) (int, error) {
	if os.Getenv("AGY_SWITCH_NO_REASSERT") == "" {
		store := keyring.DefaultStore(paths)
		pm := profile.NewManager(paths, store)
		if err := pm.ReassertCurrent(); err != nil {
			return 1, fmt.Errorf("restore selected account: %w", err)
		}
	}

	realAgy, err := FindRealAgy(paths)
	if err != nil {
		return 1, err
	}

	store := proxy.NewStore(paths)
	proxyEnv, err := BuildProxyEnv(paths, store)
	if err != nil {
		return 1, fmt.Errorf("build proxy env: %w", err)
	}

	cmd := exec.Command(realAgy, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), proxyEnv...)

	// Signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer signal.Stop(sigChan)

	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("start %s: %w", realAgy, err)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-sigChan:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(sig)
				}
			}
		}
	}()

	waitErr := cmd.Wait()
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, waitErr
	}

	return 0, nil
}
