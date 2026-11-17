package bridge_test

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func TestBridgeServer_PingAndAuth(t *testing.T) {
	tmpDir := t.TempDir()
	paths := &config.Paths{
		DataDir: tmpDir,
	}

	upstream := &proxy.Config{
		Protocol: "http",
		Server:   "127.0.0.1",
		Port:     80,
	}

	srv, err := bridge.NewServer(paths, upstream, 0)
	if err != nil {
		t.Fatalf("new server failed: %v", err)
	}
	defer srv.Stop()

	go func() {
		_ = srv.Start()
	}()

	// Wait for listener to be ready
	var port int
	for i := 0; i < 20; i++ {
		port = srv.Port()
		if port > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("server failed to bind port")
	}

	token := srv.Token()
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("agy-switch:"+token))

	// Test Ping with valid auth
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf("GET http://agy-switch.bridge/ping HTTP/1.1\r\nHost: agy-switch.bridge\r\nProxy-Authorization: %s\r\nConnection: close\r\n\r\n", authHeader)
	_, _ = conn.Write([]byte(req))

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Test with invalid auth
	connBad, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial bad: %v", err)
	}
	defer connBad.Close()

	reqBad := "GET http://agy-switch.bridge/ping HTTP/1.1\r\nHost: agy-switch.bridge\r\nProxy-Authorization: Basic bad\r\nConnection: close\r\n\r\n"
	_, _ = connBad.Write([]byte(reqBad))

	respBad, err := http.ReadResponse(bufio.NewReader(connBad), nil)
	if err != nil {
		t.Fatalf("read bad resp: %v", err)
	}
	if respBad.StatusCode != 407 {
		t.Errorf("expected 407 for bad auth, got %d", respBad.StatusCode)
	}
}

func TestStopDaemonDoesNotKillStoredPID(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	if err := bridge.SaveState(paths, &bridge.State{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.StopDaemon(paths); err != nil {
		t.Fatal(err)
	}
}

func TestBridge_LockRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	lock1 := bridge.NewLock(lockPath)
	acquired, err := lock1.TryLock()
	if err != nil || !acquired {
		t.Fatalf("lock1 acquire failed: %v", err)
	}

	// Second lock on same file should fail
	lock2 := bridge.NewLock(lockPath)
	acquired2, _ := lock2.TryLock()
	if acquired2 {
		t.Errorf("lock2 should not acquire when lock1 is held")
	}

	// Release lock1
	_ = lock1.Unlock()

	// lock2 should now succeed
	acquired3, err := lock2.TryLock()
	if err != nil || !acquired3 {
		t.Errorf("lock2 should succeed after lock1 unlocked: %v", err)
	}
	_ = lock2.Unlock()
}
