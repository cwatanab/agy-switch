package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func TestDetachedBridgeSurvivesOwnerAndStopsIndependently(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux subprocess and shell integration fixture")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "agy-switch")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agy-switch")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s: %v", output, err)
	}
	dataRoot := filepath.Join(dir, "data")
	paths := &config.Paths{DataDir: filepath.Join(dataRoot, "agy-switch")}
	store := proxy.NewStore(paths)
	if err := store.Save(proxy.Config{Name: "test", Server: "127.0.0.1", Port: 9, Protocol: "socks5h", Active: true}); err != nil {
		t.Fatal(err)
	}
	fakeAgy := filepath.Join(dir, "real-agy")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AGY_TEST_ARGS\"\nwhile [ ! -f \"$AGY_TEST_RELEASE\" ]; do sleep 0.02; done\nexit 7\n"
	if err := os.WriteFile(fakeAgy, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "XDG_DATA_HOME="+dataRoot, "AGY_BIN="+fakeAgy, "AGY_SWITCH_NO_REASSERT=1")
	t.Cleanup(func() { _ = bridge.StopDaemon(paths) })
	type owner struct {
		cmd     *exec.Cmd
		done    chan error
		release string
	}
	startOwner := func(name string) owner {
		t.Helper()
		release := filepath.Join(dir, name+"-release")
		argsFile := filepath.Join(dir, name+"-args")
		cmd := exec.Command(binary, "shim-exec", "--model", "gemini", "--help", "a b")
		cmd.Env = append(append([]string(nil), env...), "AGY_TEST_ARGS="+argsFile, "AGY_TEST_RELEASE="+release)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			_ = os.WriteFile(release, nil, 0600)
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = cmd.Process.Kill()
			}
		})
		for i := 0; i < 200; i++ {
			if args, err := os.ReadFile(argsFile); err == nil {
				if string(args) != "--model\ngemini\n--help\na b\n" {
					t.Fatalf("flags were not forwarded: %q", args)
				}
				return owner{cmd: cmd, done: done, release: release}
			}
			select {
			case err := <-done:
				t.Fatalf("owner exited before agy started: %v", err)
			default:
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("owner did not start agy")
		return owner{}
	}
	first := startOwner("first")
	st, err := bridge.LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if st.PID == first.cmd.Process.Pid || st.PID == os.Getpid() {
		t.Fatal("bridge shares its owner's process")
	}
	second := startOwner("second")
	st2, err := bridge.LoadState(paths)
	if err != nil || st2.PID != st.PID || st2.Port != st.Port || st2.Token != st.Token {
		t.Fatal("second session did not reuse the bridge")
	}
	if err := os.WriteFile(first.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first.done:
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 7 {
			t.Fatalf("child exit code was not preserved: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first owner did not exit")
	}
	if !bridge.Ping(st.Port, st.Token, time.Second) {
		t.Fatal("bridge died with its first owner")
	}
	stop := exec.Command(binary, "bridge", "stop")
	stop.Env = env
	if output, err := stop.CombinedOutput(); err != nil || !strings.Contains(string(output), "Bridge stopped") {
		t.Fatalf("stop failed: %s, %v", output, err)
	}
	if bridge.Ping(st.Port, st.Token, 200*time.Millisecond) {
		t.Fatal("bridge is still accepting connections")
	}
	select {
	case err := <-second.done:
		t.Fatalf("bridge stop terminated a live session: %v", err)
	default:
	}
}
