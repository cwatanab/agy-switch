package bridge

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func startTestBridge(t *testing.T, paths *config.Paths, cfg *proxy.Config, opts ServerOptions) *Server {
	t.Helper()
	server, err := NewServerWithOptions(paths, cfg, 0, opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Start() }()
	t.Cleanup(func() {
		_ = server.Stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	for i := 0; i < 100; i++ {
		if port := server.Port(); Ping(port, server.Token(), 50*time.Millisecond) {
			return server
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bridge did not become ready")
	return nil
}

func tunnel(t *testing.T, server *Server, destination, payload string) (net.Conn, *bufio.Reader, error) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", server.Port()), time.Second)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte(BridgeUser + ":" + server.Token()))
	header := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n%s", destination, destination, auth, payload)
	if _, err := io.WriteString(conn, header); err != nil {
		conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if !strings.Contains(status, "200") {
		conn.Close()
		return nil, nil, fmt.Errorf("CONNECT failed: %s", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, reader, nil
}

func halfCloseReply(t *testing.T, server *Server, dest, payload string) (string, error) {
	t.Helper()
	conn, reader, err := tunnel(t, server, dest, payload)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		return "", err
	}
	data, err := io.ReadAll(reader)
	return string(data), err
}

func replyServer(t *testing.T, label string, socks bool) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if socks {
					var greeting [2]byte
					if _, err := io.ReadFull(conn, greeting[:]); err != nil {
						return
					}
					if _, err := io.CopyN(io.Discard, conn, int64(greeting[1])); err != nil {
						return
					}
					if _, err := conn.Write([]byte{5, 0}); err != nil {
						return
					}
					var header [4]byte
					if _, err := io.ReadFull(conn, header[:]); err != nil {
						return
					}
					size := 0
					switch header[3] {
					case 1:
						size = 4
					case 4:
						size = 16
					case 3:
						var n [1]byte
						if _, err := io.ReadFull(conn, n[:]); err != nil {
							return
						}
						size = int(n[0])
					default:
						return
					}
					if _, err := io.CopyN(io.Discard, conn, int64(size+2)); err != nil {
						return
					}
					if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
						return
					}
				}
				data, err := io.ReadAll(conn)
				if err == nil {
					_, _ = io.WriteString(conn, label+string(data))
				}
			}()
		}
	}()
	return listener
}

func waitForNoTunnels(t *testing.T, server *Server) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if server.ActiveConnections() == 0 && server.active.Load() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("connections remain: %d, requests: %d", server.ActiveConnections(), server.active.Load())
}

func TestConnectHalfCloseAndBufferedBytes(t *testing.T) {
	backend := replyServer(t, "reply:", false)
	server := startTestBridge(t, &config.Paths{DataDir: t.TempDir()}, nil, ServerOptions{})
	reply, err := halfCloseReply(t, server, backend.Addr().String(), "already-buffered")
	if err != nil || reply != "reply:already-buffered" {
		t.Fatalf("reply=%q, error=%v", reply, err)
	}
	waitForNoTunnels(t, server)
}

func TestConnectReleasesOneThousandTunnels(t *testing.T) {
	backend := replyServer(t, "ok:", false)
	server := startTestBridge(t, &config.Paths{DataDir: t.TempDir()}, nil, ServerOptions{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				reply, err := halfCloseReply(t, server, backend.Addr().String(), "payload")
				if err != nil || reply != "ok:payload" {
					t.Errorf("reply=%q, error=%v", reply, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	waitForNoTunnels(t, server)
}

func TestIdleTunnelAndStopReleaseConnections(t *testing.T) {
	backend := replyServer(t, "", false)
	server := startTestBridge(t, &config.Paths{DataDir: t.TempDir()}, nil, ServerOptions{TunnelIdleTimeout: 40 * time.Millisecond})
	conn, reader, err := tunnel(t, server, backend.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("idle connection did not close")
	}
	waitForNoTunnels(t, server)
	conn2, _, err := tunnel(t, server, backend.Addr().String(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	waitForNoTunnels(t, server)
}

func TestProxyEditsApplyToNewTunnels(t *testing.T) {
	first, second := replyServer(t, "first:", true), replyServer(t, "second:", true)
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)
	cfg := proxy.Config{Name: "route", Server: "127.0.0.1", Port: first.Addr().(*net.TCPAddr).Port, Protocol: "socks5h", Active: true}
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	server := startTestBridge(t, paths, &cfg, ServerOptions{})
	old, oldReader, err := tunnel(t, server, "unresolved.test:443", "old")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	cfg.Port = second.Addr().(*net.TCPAddr).Port
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	reply, err := halfCloseReply(t, server, "unresolved.test:443", "new")
	if err != nil || reply != "second:new" {
		t.Fatalf("new route reply=%q, error=%v", reply, err)
	}
	if err := old.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(oldReader)
	if err != nil || string(data) != "first:old" {
		t.Fatalf("existing tunnel changed route: %q, %v", data, err)
	}
	if err := store.SetActive(cfg.Name, false); err != nil {
		t.Fatal(err)
	}
	reply, err = halfCloseReply(t, server, "unresolved.test:443", "existing-session")
	if err != nil || reply != "second:existing-session" {
		t.Fatalf("last route reply=%q, error=%v", reply, err)
	}
	waitForNoTunnels(t, server)
}
