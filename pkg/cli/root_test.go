package cli

import (
	"bytes"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

func testPaths(t *testing.T) *config.Paths {
	t.Helper()
	dir := t.TempDir()
	return &config.Paths{
		DataDir: dir, ProfilesDir: filepath.Join(dir, "profiles"),
		LiveToken: filepath.Join(dir, "live-token"), LegacyToken: filepath.Join(dir, "legacy-token"),
		BinDir: filepath.Join(dir, "bin"),
	}
}

func TestProxyAddPersistsAndRejectsDuplicate(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("AGY_TEST_PROXY_PASSWORD", "test-password")
	args := []string{"proxy", "add", "work", "--server", "127.0.0.1", "--port", "1080", "--protocol", "socks5h", "--username", "alice", "--password-env", "AGY_TEST_PROXY_PASSWORD"}
	cmd := BuildRootCmd(paths)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	list, err := proxy.NewStore(paths).List()
	if err != nil || len(list) != 1 {
		t.Fatalf("proxy was not saved: %v", err)
	}
	got := list[0]
	if got.GetServer() != "127.0.0.1" || got.Port != 1080 || got.Protocol != "socks5h" || got.Password != "test-password" || got.Active {
		t.Fatal("unexpected saved proxy fields")
	}
	if strings.Contains(output.String(), "test-password") {
		t.Fatal("password leaked to output")
	}
	before, err := os.ReadFile(filepath.Join(paths.DataDir, "proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	cmd = BuildRootCmd(paths)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err == nil {
		t.Fatal("duplicate proxy was accepted")
	}
	after, err := os.ReadFile(filepath.Join(paths.DataDir, "proxy.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("duplicate add modified existing configuration")
	}
}

func TestProxyAddRejectsInvalidConfig(t *testing.T) {
	for _, extra := range [][]string{
		{"--server", "", "--port", "80"},
		{"--server", "localhost", "--port", "65536"},
		{"--server", "localhost", "--port", "80", "--protocol", "invalid"},
	} {
		paths := testPaths(t)
		cmd := BuildRootCmd(paths)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"proxy", "add", "work"}, extra...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("invalid configuration accepted: %v", extra)
		}
		if _, err := os.Stat(filepath.Join(paths.DataDir, "proxy.json")); !os.IsNotExist(err) {
			t.Fatal("invalid configuration persisted")
		}
	}
}

func TestShimExecForwardsFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	paths := testPaths(t)
	script := filepath.Join(paths.DataDir, "real agy")
	argsFile := filepath.Join(paths.DataDir, "args")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AGY_TEST_ARGS\"\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGY_BIN", script)
	t.Setenv("AGY_TEST_ARGS", argsFile)
	t.Setenv("AGY_SWITCH_NO_REASSERT", "1")
	cmd := BuildRootCmd(paths)
	cmd.SetArgs([]string{"shim-exec", "--model", "gemini", "--help", "a b", "--", "-x"})
	err := cmd.Execute()
	if childErr, ok := err.(childExitError); !ok || childErr.code != 7 {
		t.Fatalf("child exit status lost: %v", err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil || string(got) != "--model\ngemini\n--help\na b\n--\n-x\n" {
		t.Fatalf("arguments changed: %q, %v", got, err)
	}
}

type mockTransport func(*http.Request) (*http.Response, error)

func (f mockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoginPreservesExistingAccounts(t *testing.T) {
	for _, alreadySaved := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsaved", true: "name collision"}[alreadySaved], func(t *testing.T) {
			paths := testPaths(t)
			if err := config.EnsureDirs(paths); err != nil {
				t.Fatal(err)
			}
			old := []byte(`{"token":{"access_token":"old","refresh_token":"rt-old"},"email":"shared@old.test"}`)
			if err := os.WriteFile(paths.LiveToken, old, 0600); err != nil {
				t.Fatal(err)
			}
			if alreadySaved {
				if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "shared.json"), old, 0600); err != nil {
					t.Fatal(err)
				}
			}
			newToken := `{"token":{"access_token":"new","refresh_token":"rt-new"},"email":"shared@new.test"}`
			store := keyring.NewFileStore(paths)
			client := &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
			})}
			cmd := NewCommand(paths, Dependencies{
				TokenStore: store, HTTPClient: client,
				FindAgy:    func(*config.Paths) (string, error) { return "agy", nil },
				RunProcess: func(*exec.Cmd) error { return store.WriteLive([]byte(newToken)) },
			})

			cmd.SetOut(io.Discard)
			cmd.SetArgs([]string{"login"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(paths.ProfilesDir, "shared.json"))
			if err != nil || !bytes.Equal(got, old) {
				t.Fatal("old login was lost")
			}
			got, err = os.ReadFile(filepath.Join(paths.ProfilesDir, "shared-2.json"))
			if err != nil || string(got) != newToken {
				t.Fatal("new account was not saved separately")
			}
		})
	}
}
