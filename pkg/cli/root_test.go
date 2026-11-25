package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
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
	cmd := NewCommand(paths, Dependencies{DisableNetCheck: true})
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
	cmd = NewCommand(paths, Dependencies{DisableNetCheck: true})
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
		cmd := NewCommand(paths, Dependencies{DisableNetCheck: true})
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
				FindAgy:         func(*config.Paths) (string, error) { return "agy", nil },
				RunProcess:      func(*exec.Cmd) error { return store.WriteLive([]byte(newToken)) },
				DisableNetCheck: true,
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

func TestLsReportsQuotaErrorAndRefreshFlag(t *testing.T) {
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_ID", "")
	t.Setenv("AGY_SWITCH_OAUTH_CLIENT_SECRET", "")

	paths := testPaths(t)
	if err := config.EnsureDirs(paths); err != nil {
		t.Fatal(err)
	}
	prof := []byte(`{"token":{"refresh_token":"rt-only"},"email":"user@test.local"}`)
	if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "main.json"), prof, 0600); err != nil {
		t.Fatal(err)
	}
	cacheJSON := fmt.Sprintf(`{"user@test.local":{"tier":"Pro","gemini_5h_fraction":0.8,"has_gemini":true,"updated_at":%q}}`,
		time.Now().Add(-time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(paths.DataDir, "quota_cache.json"), []byte(cacheJSON), 0600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"ls"}, {"ls", "-r"}} {
		var stdout, stderr bytes.Buffer
		cmd := NewCommand(paths, Dependencies{
			TokenStore:      keyring.NewFileStore(paths),
			FindAgy:         func(*config.Paths) (string, error) { return "", os.ErrNotExist },
			DisableNetCheck: true,
		})
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
		if !strings.Contains(stdout.String(), "80%") {
			t.Fatalf("%v stdout missing cached quota: %s", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "warning: quota refresh failed for") {
			t.Fatalf("%v stderr missing warning: %s", args, stderr.String())
		}
	}
}

func TestLogoutRevokeAndFailSafety(t *testing.T) {
	paths := testPaths(t)
	_ = config.EnsureDirs(paths)
	prof := []byte(`{"token":{"access_token":"at","refresh_token":"rt-revoke"},"email":"revoke@test.local"}`)
	if err := os.WriteFile(filepath.Join(paths.ProfilesDir, "main.json"), prof, 0600); err != nil {
		t.Fatal(err)
	}

	var revokeHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeHits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Note: RevokeToken hits quota.Client's default RevokeURL unless overridden, so verify via Transport rewrite.
	failingClient := &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		revokeHits++
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	cmd := NewCommand(paths, Dependencies{
		TokenStore:      keyring.NewFileStore(paths),
		HTTPClient:      failingClient,
		DisableNetCheck: true,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"logout", "main", "--revoke"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected logout --revoke to fail when revoke endpoint fails")
	}
	if _, err := os.Stat(filepath.Join(paths.ProfilesDir, "main.json")); err != nil {
		t.Fatal("profile should not be deleted when revoke fails")
	}

	var revokedToken string
	okClient := &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		revokedToken = string(body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	cmd2 := NewCommand(paths, Dependencies{
		TokenStore:      keyring.NewFileStore(paths),
		HTTPClient:      okClient,
		DisableNetCheck: true,
	})
	var out bytes.Buffer
	cmd2.SetOut(&out)
	cmd2.SetArgs([]string{"logout", "main", "--revoke"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("logout --revoke failed: %v", err)
	}
	if !strings.Contains(revokedToken, "token=rt-revoke") || !strings.Contains(out.String(), "Grant revoked") {
		t.Fatalf("expected revoke request & output, got token=%q out=%q", revokedToken, out.String())
	}
	if _, err := os.Stat(filepath.Join(paths.ProfilesDir, "main.json")); !os.IsNotExist(err) {
		t.Fatal("profile should be deleted after successful revoke")
	}
}

func TestUseWarnsRunningAgyAndLsWarnsClobber(t *testing.T) {
	paths := testPaths(t)
	_ = config.EnsureDirs(paths)
	p1 := []byte(`{"token":{"access_token":"at-1","refresh_token":"rt-1"},"email":"one@test.local"}`)
	p2 := []byte(`{"token":{"access_token":"at-2","refresh_token":"rt-2"},"email":"two@test.local"}`)
	_ = os.WriteFile(filepath.Join(paths.ProfilesDir, "one.json"), p1, 0600)
	_ = os.WriteFile(filepath.Join(paths.ProfilesDir, "two.json"), p2, 0600)
	store := keyring.NewFileStore(paths)
	_ = store.WriteLive(p1)

	var stdout, stderr bytes.Buffer
	cmd := NewCommand(paths, Dependencies{
		TokenStore:        store,
		CountAgyProcesses: func() (int, error) { return 2, nil },
		DisableNetCheck:   true,
	})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"use", "one"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "2 agy session(s) still running") {
		t.Fatalf("expected running agy warning, got: %s", stderr.String())
	}

	// Clobber live token with p2, then run `ls`
	_ = store.WriteLive(p2)
	mgr := profile.NewManager(paths, store)
	_ = mgr.SetCurrent("one")
	cache, _ := json.Marshal(map[string]interface{}{
		"one@test.local": map[string]interface{}{"tier": "Pro", "has_gemini": true, "gemini_5h_fraction": 0.9, "updated_at": time.Now().Format(time.RFC3339)},
		"two@test.local": map[string]interface{}{"tier": "Free", "has_gemini": true, "gemini_5h_fraction": 0.5, "updated_at": time.Now().Format(time.RFC3339)},
	})
	_ = os.WriteFile(filepath.Join(paths.DataDir, "quota_cache.json"), cache, 0600)

	stdout.Reset()
	stderr.Reset()
	cmd2 := NewCommand(paths, Dependencies{
		TokenStore:      store,
		DisableNetCheck: true,
	})
	cmd2.SetOut(&stdout)
	cmd2.SetErr(&stderr)
	cmd2.SetArgs([]string{"ls"})
	if err := cmd2.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "live login was overwritten") {
		t.Fatalf("expected clobber warning in ls stderr, got: %s", stderr.String())
	}
}

func TestNetCheckWarningAndLoginPrecheck(t *testing.T) {
	paths := testPaths(t)
	_ = config.EnsureDirs(paths)

	var stderr bytes.Buffer
	cmd := NewCommand(paths, Dependencies{
		TokenStore: keyring.NewFileStore(paths),
		NetCheck:   func(context.Context) (string, string) { return proxy.NetStateRegion, "CN" },
		IsTTY:      func() bool { return false },
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "IP region (CN) is not supported") {
		t.Fatalf("expected region warning, got: %s", stderr.String())
	}

	// Non-TTY login should abort on unsupported region
	stderr.Reset()
	cmdLogin := NewCommand(paths, Dependencies{
		TokenStore: keyring.NewFileStore(paths),
		NetCheck:   func(context.Context) (string, string) { return proxy.NetStateRegion, "CN" },
		IsTTY:      func() bool { return false },
		FindAgy:    func(*config.Paths) (string, error) { return "agy", nil },
	})
	cmdLogin.SetOut(io.Discard)
	cmdLogin.SetErr(&stderr)
	cmdLogin.SetArgs([]string{"login"})
	if err := cmdLogin.Execute(); err == nil {
		t.Fatal("expected non-TTY login to fail in unsupported region")
	}

	// Interactive TTY login prompts for profile name when no arg given
	store := keyring.NewFileStore(paths)
	newToken := `{"token":{"access_token":"at-new","refresh_token":"rt-new"},"email":"alice@example.com"}`
	cmdLoginTTY := NewCommand(paths, Dependencies{
		TokenStore:      store,
		DisableNetCheck: true,
		IsTTY:           func() bool { return true },
		FindAgy:         func(*config.Paths) (string, error) { return "agy", nil },
		RunProcess:      func(*exec.Cmd) error { return store.WriteLive([]byte(newToken)) },
		HTTPClient: &http.Client{Transport: mockTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		})},
	})
	cmdLoginTTY.SetIn(strings.NewReader("my-work\n"))
	cmdLoginTTY.SetOut(io.Discard)
	cmdLoginTTY.SetArgs([]string{"login"})
	if err := cmdLoginTTY.Execute(); err != nil {
		t.Fatalf("interactive login failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.ProfilesDir, "my-work.json")); err != nil {
		t.Fatalf("expected profile my-work.json to be created: %v", err)
	}
}

func TestProxyEditRenameDeleteAndStatus(t *testing.T) {
	paths := testPaths(t)
	_ = config.EnsureDirs(paths)
	t.Setenv("AGY_TEST_PW", "pw2")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := NewCommand(paths, Dependencies{
			DisableNetCheck: true,
			FindAgy:         func(*config.Paths) (string, error) { return "/bin/sh", nil },
		})
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}

	if _, err := run("proxy", "add", "p1", "--server", "1.2.3.4", "--port", "1080", "--protocol", "socks5h", "--validity", "30d", "--fallback"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("proxy", "edit", "p1", "--port", "2080", "--password-env", "AGY_TEST_PW", "--validity", "never"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("proxy", "rename", "p1", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("proxy", "on"); err != nil {
		t.Fatalf("single-proxy auto-on failed: %v", err)
	}
	statusOut, err := run("proxy", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOut, "work") || !strings.Contains(statusOut, "2080") || !strings.Contains(statusOut, "shim: installed") || !strings.Contains(statusOut, "bridge:") {
		t.Fatalf("unexpected proxy status output:\n%s", statusOut)
	}
	if _, err := run("proxy", "delete", "work"); err != nil {
		t.Fatal(err)
	}
	if list, err := proxy.NewStore(paths).List(); err != nil || len(list) != 0 {
		t.Fatalf("expected empty proxy list after delete, got %+v (%v)", list, err)
	}
}
