package bridge

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type State struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func statePath(paths *config.Paths) string {
	return filepath.Join(paths.DataDir, "bridge.json")
}

func LoadState(paths *config.Paths) (*State, error) {
	data, err := os.ReadFile(statePath(paths))
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func SaveState(paths *config.Paths, st *State) error {
	if err := config.EnsureDirs(paths); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(statePath(paths), data, 0600)
}

func RemoveState(paths *config.Paths) {
	_ = os.Remove(statePath(paths))
}

func Ping(port int, token string, timeout time.Duration) bool {
	return control(port, token, "GET", "/ping", timeout)
}

func control(port int, token, method, path string, timeout time.Duration) bool {
	if port <= 0 || token == "" {
		return false
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", BridgeUser, token)))
	req := fmt.Sprintf("%s http://agy-switch.bridge%s HTTP/1.1\r\nHost: agy-switch.bridge\r\nProxy-Authorization: %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", method, path, authHeader)
	if _, err := conn.Write([]byte(req)); err != nil {
		return false
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

func Status(paths *config.Paths) (bool, int, error) {
	st, err := LoadState(paths)
	if err != nil {
		return false, 0, nil
	}
	if Ping(st.Port, st.Token, 1*time.Second) {
		return true, st.Port, nil
	}
	return false, st.Port, nil
}
