# Golang Port & Windows Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Completely port `agy-switch` from Python to Go with full feature parity, native Windows support (Windows Credential Manager, Windows Terminal TUI, process monitoring, and binary shim), and single-binary direct distribution.

**Architecture:** Pure Go modular CLI application using `spf13/cobra` for command routing and `charmbracelet/bubbletea` for cross-platform interactive terminal pickers. Cross-platform abstractions handle filesystem paths, credential storage (WinCred / macOS Keychain / file), process management, and transparent proxy shimming.

**Tech Stack:** Go 1.24+, `spf13/cobra`, `charmbracelet/bubbletea`, `charmbracelet/lipgloss`, `gofrs/flock`, `golang.org/x/net/proxy`, `golang.org/x/sys/windows`.

**Spec:** [docs/superpowers/specs/2026-11-17-golang-windows-port-design.md](file:///workspaces/agy-switch/docs/superpowers/specs/2026-11-17-golang-windows-port-design.md)

---

## Global Constraints

- **Pure Go / Zero CGO:** `CGO_ENABLED=0` must succeed for all targets (`windows/amd64`, `linux/amd64`, `darwin/amd64`, `darwin/arm64`).
- **Backward Compatibility:** Reads and writes existing profile files in `$XDG_DATA_HOME/agy-switch/profiles/` and proxy configs without format migrations.
- **Credential Safety:** All credentials and tokens written to disk must have `0600` permissions. Tokens must never be output to stdout or stderr.
- **Zero Runtime Dependencies:** Does not require Python 3, Node.js, or external CLI tools (`curl`, `ps`, `pgrep`) at runtime.
- **CLI Invariants:** `asw` with no arguments launches the interactive account picker; `asw <cmd>` and `agy-switch <cmd>` behave identically.

---

## Review Focus

1. **Native Keyring Fallback:** When Windows Credential Manager or macOS Keychain is inaccessible (e.g. headless SSH or permission denial), credential operations must seamlessly fall back to the live token file (`~/.gemini/antigravity-cli/antigravity-oauth-token`) without crashing.
2. **Corrupted JSON Resilience:** If any profile or proxy JSON file contains malformed data, operations must log a warning and skip the corrupted item without panicking or wiping other profiles.
3. **Stale Bridge Lock Recovery:** If the bridge process terminates abruptly, `gofrs/flock` must allow the next bridge instance to acquire the lock and restart smoothly.
4. **Proxy Probing Timeout:** If all candidate protocols (`https`, `socks5h`, `http`) time out during proxy probing, the CLI must cleanly report failure within 5 seconds without hanging.
5. **Windows Shim Infinite Loop Prevention:** The Windows binary shim `agy.exe` must resolve the real `agy.exe` path outside of `%LOCALAPPDATA%\agy-switch\bin` to avoid recursively executing itself.

---

## File Structure

```
agy-switch/
├── go.mod
├── go.sum
├── Makefile
├── cmd/
│   ├── agy-switch/
│   │   └── main.go       # Root Cobra command and CLI entrypoint
│   └── asw/
│       └── main.go       # Thin entrypoint alias executing root command
├── pkg/
│   ├── config/
│   │   ├── paths.go      # XDG and Windows %LOCALAPPDATA% path resolutions
│   │   └── paths_test.go
│   ├── keyring/
│   │   ├── keyring.go    # Abstract TokenStore interface
│   │   ├── file.go       # File-based token storage (~/.gemini/...)
│   │   ├── windows.go    # Win32 CredRead/CredWrite implementation
│   │   ├── darwin.go     # macOS security CLI implementation
│   │   └── keyring_test.go
│   ├── profile/
│   │   ├── token.go      # OAuth token JSON parsing, JWT decode, expiry
│   │   ├── store.go      # Profile snapshot, restore, rename, delete, stash
│   │   ├── profile_test.go
│   │   └── token_test.go
│   ├── quota/
│   │   ├── client.go     # Google Cloud Code API client & token refresher
│   │   ├── oauth.go      # Static OAuth candidate pair & binary regex scanner
│   │   ├── cache.go      # Quota disk caching logic (15min TTL)
│   │   └── quota_test.go
│   ├── proxy/
│   │   ├── model.go      # Proxy configuration data structures
│   │   ├── store.go      # CRUD operations for proxies.json & active state
│   │   ├── racer.go      # Protocol probe racer (https/socks5h/http)
│   │   ├── geo.go        # Country code probe (api.country.is)
│   │   └── proxy_test.go
│   ├── bridge/
│   │   ├── server.go     # HTTP-to-SOCKS5 forward proxy server
│   │   ├── auth.go       # Local bearer token generation and verification
│   │   ├── lock.go       # Cross-platform file locking (gofrs/flock)
│   │   ├── lifecycle.go  # Idle timer & process counter (Toolhelp32 on Win)
│   │   └── bridge_test.go
│   ├── shim/
│   │   ├── shim.go       # Linux bash script and Windows agy.exe installer
│   │   ├── run.go        # Shim execution handler (env injection & exec)
│   │   └── shim_test.go
│   └── tui/
│       ├── picker.go     # Bubble Tea model for account switcher
│       ├── proxy.go      # Bubble Tea model for proxy manager
│       ├── style.go      # Lipgloss styles and color palette
│       └── tui_test.go
```

---

## Tasks

### Task 1: Project Scaffolding & Configuration Paths (`pkg/config`)

**Files:**
- Create: `go.mod`
- Create: `pkg/config/paths.go`
- Test: `pkg/config/paths_test.go`

**Interfaces:**
- Produces:
  ```go
  package config

  type Paths struct {
      HomeDir     string
      DataDir     string
      ProfilesDir string
      LiveToken   string
      LegacyToken string
      BinDir      string
      ProxyEnv    string
      BridgeLock  string
      BridgeLog   string
  }
  func ResolvePaths() (*Paths, error)
  func EnsureDirs(p *Paths) error
  ```

- [ ] **Step 1: Write the failing test for `ResolvePaths`**

```go
// pkg/config/paths_test.go
package config_test

import (
    "os"
    "path/filepath"
    "testing"
    "github.com/ZhangNy301/agy-switch/pkg/config"
)

func testResolvePaths_ContainsDataAndProfiles(t *testing.T) {
    p, err := config.ResolvePaths()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if p.DataDir == "" || p.ProfilesDir == "" || p.LiveToken == "" {
        t.Errorf("expected non-empty paths, got: %+v", p)
    }
    if p.ProfilesDir != filepath.Join(p.DataDir, "profiles") {
        t.Errorf("expected profiles dir inside data dir, got %s", p.ProfilesDir)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config/...`  
Expected: FAIL with module/package not found.

- [ ] **Step 3: Implement `go.mod` and `pkg/config/paths.go`**

Initialize module `github.com/ZhangNy301/agy-switch`. Implement `ResolvePaths()` detecting Windows `%LOCALAPPDATA%` / `%USERPROFILE%` and Unix `$XDG_DATA_HOME` / `$HOME`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/config/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod pkg/config
git commit -m "feat(config): add cross-platform path resolution"
```

---

### Task 2: Credential Keyring & Token Management (`pkg/keyring`, `pkg/profile`)

**Files:**
- Create: `pkg/keyring/keyring.go`
- Create: `pkg/keyring/file.go`
- Create: `pkg/keyring/windows.go` (build tag `windows`)
- Create: `pkg/keyring/darwin.go` (build tag `darwin`)
- Create: `pkg/keyring/other.go` (build tag `!windows,!darwin`)
- Create: `pkg/profile/token.go`
- Create: `pkg/profile/store.go`
- Test: `pkg/keyring/keyring_test.go`
- Test: `pkg/profile/token_test.go`
- Test: `pkg/profile/profile_test.go`

**Interfaces:**
- Consumes: `pkg/config.Paths`
- Produces:
  ```go
  package keyring
  type TokenStore interface {
      ReadLive() ([]byte, error)
      WriteLive(data []byte) error
      DeleteLive() error
  }
  func DefaultStore(paths *config.Paths) TokenStore

  package profile
  type Token struct {
      AccessToken  string    `json:"access_token"`
      RefreshToken string    `json:"refresh_token"`
      ExpiresAt    time.Time `json:"expires_at"`
      Email        string    `json:"email"`
  }
  type Profile struct {
      Name      string    `json:"name"`
      Email     string    `json:"email"`
      Tier      string    `json:"tier"`
      LastUsed  time.Time `json:"last_used"`
      Token     Token     `json:"token"`
  }
  type Manager struct { ... }
  func NewManager(paths *config.Paths, store keyring.TokenStore) *Manager
  func (m *Manager) List() ([]Profile, error)
  func (m *Manager) Active() (string, error)
  func (m *Manager) Use(name string) error
  func (m *Manager) Snapshot(name string) error
  func (m *Manager) Rename(oldName, newName string) error
  func (m *Manager) Delete(name string) error
  func (m *Manager) Stash() error
  func (m *Manager) Unstash() error
  ```

- [ ] **Step 1: Write failing tests for token parsing & profile store**

Write test verifying:
1. `profile.ParseTokenJSON([]byte)` parses access token, decodes JWT payload for email address, and handles expiry.
2. `profile.Manager.Snapshot` saves profile to `$ProfilesDir/<name>.json` with `0600` permissions.
3. `profile.Manager.Use` writes snapshot back to live token store.
4. Review Focus #1: Keyring store falls back to file on error.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/profile/... ./pkg/keyring/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/keyring` and `pkg/profile`**

Implement JWT email decoder (split by `.`, base64 URL decode payload), atomic file writer with `0600` permissions, and platform-specific keyring wrappers.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/profile/... ./pkg/keyring/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/keyring pkg/profile
git commit -m "feat(profile): implement token decoding and profile management"
```

---

### Task 3: Google AI Quota Polling & OAuth Discovery (`pkg/quota`)

**Files:**
- Create: `pkg/quota/oauth.go`
- Create: `pkg/quota/client.go`
- Create: `pkg/quota/cache.go`
- Test: `pkg/quota/quota_test.go`

**Interfaces:**
- Consumes: `pkg/config.Paths`, `pkg/profile.Token`
- Produces:
  ```go
  package quota
  type QuotaInfo struct {
      Tier          string    `json:"tier"`
      Gemini5hUsed  int       `json:"gemini_5h_used"`
      Gemini5hLimit int       `json:"gemini_5h_limit"`
      Claude5hUsed  int       `json:"claude_5h_used"`
      Claude5hLimit int       `json:"claude_5h_limit"`
      WeeklyUsed    int       `json:"weekly_used"`
      WeeklyLimit   int       `json:"weekly_limit"`
      UpdatedAt     time.Time `json:"updated_at"`
  }
  type Client struct { ... }
  func NewClient(paths *config.Paths) *Client
  func (c *Client) Fetch(tok *profile.Token, forceRefresh bool) (*QuotaInfo, error)
  ```

- [ ] **Step 1: Write failing test with mock Google API HTTP server**

Set up `httptest.Server` returning Cloud Code quota payload and verify `Client.Fetch()` extracts tier and quota limits, caching result to disk. Verify Review Focus #2 (graceful fallback on invalid JSON).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/quota/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/quota`**

Implement embedded known OAuth client ID/secret, binary scanner fallback for real `agy` executable, HTTP API caller to `daily-cloudcode-pa.googleapis.com`, and disk caching with 15-minute TTL.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/quota/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/quota
git commit -m "feat(quota): implement quota fetcher and oauth discovery"
```

---

### Task 4: Proxy Manager, Protocol Racer & Geo Probe (`pkg/proxy`)

**Files:**
- Create: `pkg/proxy/model.go`
- Create: `pkg/proxy/store.go`
- Create: `pkg/proxy/racer.go`
- Create: `pkg/proxy/geo.go`
- Test: `pkg/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `pkg/config.Paths`
- Produces:
  ```go
  package proxy
  type Config struct {
      Name      string `json:"name"`
      Server    string `json:"server"`
      Port      int    `json:"port"`
      Protocol  string `json:"protocol"` // https, socks5h, http
      Username  string `json:"username,omitempty"`
      Password  string `json:"password,omitempty"`
      Country   string `json:"country,omitempty"`
      Active    bool   `json:"active"`
  }
  type Store struct { ... }
  func NewStore(paths *config.Paths) *Store
  func (s *Store) List() ([]Config, error)
  func (s *Store) Save(p Config) error
  func (s *Store) SetActive(name string, active bool) error
  func RaceProtocol(server string, port int, user, pass string) (string, error)
  func ProbeGeo(proxyURL string) (country string, blocked bool, err error)
  ```

- [ ] **Step 1: Write failing tests for proxy store and protocol racer**

Test CRUD operations in `proxies.json`. Set up mock HTTP and SOCKS5 listeners and test `RaceProtocol` and `ProbeGeo`. Verify Review Focus #4 (clean timeout within 5s when all endpoints fail).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/proxy/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/proxy`**

Implement concurrent racer dialing target endpoint via `https`, `socks5h` (`golang.org/x/net/proxy`), and `http`. Implement GeoIP lookup against `api.country.is` and check against blocked ISO codes (`CN HK MO RU BY IR KP CU SY`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/proxy/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/proxy
git commit -m "feat(proxy): add proxy storage, protocol racing, and geo probe"
```

---

### Task 5: SOCKS Bridge & Cross-Platform Process Lifecycle (`pkg/bridge`)

**Files:**
- Create: `pkg/bridge/auth.go`
- Create: `pkg/bridge/lock.go`
- Create: `pkg/bridge/lifecycle.go`
- Create: `pkg/bridge/lifecycle_windows.go` (build tag `windows`)
- Create: `pkg/bridge/lifecycle_unix.go` (build tag `!windows`)
- Create: `pkg/bridge/server.go`
- Test: `pkg/bridge/bridge_test.go`

**Interfaces:**
- Consumes: `pkg/config.Paths`, `pkg/proxy.Config`
- Produces:
  ```go
  package bridge
  type Server struct { ... }
  func NewServer(paths *config.Paths, upstream *proxy.Config) (*Server, error)
  func (s *Server) Start() error
  func (s *Server) Stop() error
  func Status(paths *config.Paths) (running bool, port int, err error)
  func CountAgyProcesses() (int, error)
  ```

- [ ] **Step 1: Write failing test for bridge proxying**

Test starting bridge server on `127.0.0.1:0` with random auth token, routing an HTTP request through it to a mock upstream, and asserting token validation. Test Review Focus #3 (stale lock recovery with `gofrs/flock`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/bridge/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/bridge`**

Implement HTTP forward proxy with `CONNECT` tunnel handler and SOCKS5 dialer. Implement cross-platform process counting: `CreateToolhelp32Snapshot` on Windows and `/proc` scan on Linux. Implement 5-minute idle watchdog.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/bridge/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/bridge
git commit -m "feat(bridge): implement socks-to-http bridge with cross-platform lifecycle"
```

---

### Task 6: Transparent Proxy Shim (`pkg/shim`)

**Files:**
- Create: `pkg/shim/shim.go`
- Create: `pkg/shim/run.go`
- Test: `pkg/shim/shim_test.go`

**Interfaces:**
- Consumes: `pkg/config.Paths`, `pkg/proxy.Store`
- Produces:
  ```go
  package shim
  func Install(paths *config.Paths) error
  func Uninstall(paths *config.Paths) error
  func ExecuteRealAgy(paths *config.Paths, args []string) (exitCode int, err error)
  ```

- [ ] **Step 1: Write failing test for shim generation and execution**

Test installing bash script on Unix and `agy.exe` runner on Windows. Test Review Focus #5: Ensure `ExecuteRealAgy` finds the real `agy` executable and avoids self-recursion.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/shim/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/shim`**

Implement environment variable builder (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`), bridge auto-start trigger, and process forwarding with standard signals.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/shim/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/shim
git commit -m "feat(shim): add transparent proxy shim for Unix and Windows"
```

---

### Task 7: Interactive Terminal UI (`pkg/tui`)

**Files:**
- Create: `pkg/tui/style.go`
- Create: `pkg/tui/picker.go`
- Create: `pkg/tui/proxy.go`
- Test: `pkg/tui/tui_test.go`

**Interfaces:**
- Consumes: `pkg/profile.Manager`, `pkg/quota.Client`, `pkg/proxy.Store`
- Produces:
  ```go
  package tui
  func RunPicker(pm *profile.Manager, qc *quota.Client) (selectedProfile string, err error)
  func RunProxyManager(ps *proxy.Store) error
  ```

- [ ] **Step 1: Write failing test for Bubble Tea model event handling**

Test key messages (`tea.KeyMsg` for `up`, `down`, `enter`, `r`, `q`) on `PickerModel` and `ProxyModel`, asserting cursor movement and selected state.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui/...`  
Expected: FAIL.

- [ ] **Step 3: Implement `pkg/tui`**

Build Bubble Tea models using Lipgloss styling. Display email, tier badges, quota progress bars, and navigation help.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./pkg/tui/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/tui
git commit -m "feat(tui): implement interactive picker and proxy manager with bubbletea"
```

---

### Task 8: CLI Commands & Multi-call Entrypoints (`cmd/agy-switch`, `cmd/asw`)

**Files:**
- Create: `cmd/agy-switch/main.go`
- Create: `cmd/asw/main.go`
- Test: `cmd/agy-switch/cli_test.go`

**Interfaces:**
- Connects all components to Cobra CLI commands:
  - `asw` (picker)
  - `asw ls`
  - `asw login`
  - `asw use <name>`
  - `asw rename <old> <new>`
  - `asw logout [name] [--revoke]`
  - `asw proxy [on|off|status]`
  - `asw bridge [status|stop]`
  - `asw version`

- [ ] **Step 1: Write failing end-to-end CLI command tests**

Test executing CLI with `--help`, `version`, `ls`, and subcommands with mock arguments.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/agy-switch/...`  
Expected: FAIL.

- [ ] **Step 3: Implement Cobra commands and multi-call logic**

Implement `cmd/agy-switch/main.go`. If binary is invoked as `agy` or `agy.exe`, delegate directly to `shim.ExecuteRealAgy()`. If invoked as `asw` or `agy-switch` without arguments, launch `tui.RunPicker()`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./cmd/agy-switch/...`  
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/
git commit -m "feat(cli): wire up cobra commands and multi-call entrypoints"
```

---

### Task 9: Cross-Compilation, Packaging & Documentation

**Files:**
- Create: `Makefile`
- Modify: `.gitignore`
- Modify: `README.md`
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Create Makefile with cross-compilation targets**

Add targets:
- `build`: Local build for current platform.
- `build-all`: Cross-compiles for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`.
- `test`: Runs all unit tests with race detection.

- [ ] **Step 2: Verify cross-compilation succeeds**

Run:
```bash
GOOS=windows GOARCH=amd64 go build -o dist/asw.exe ./cmd/asw
GOOS=linux GOARCH=amd64 go build -o dist/asw ./cmd/asw
GOOS=darwin GOARCH=arm64 go build -o dist/asw-darwin-arm64 ./cmd/asw
```
Expected: All binaries compile with exit code 0.

- [ ] **Step 3: Update documentation and GitHub Actions release workflow**

Update `README.md` to describe Windows installation via Scoop or standalone `.zip` download alongside curl script. Add GoReleaser workflow in `.github/workflows/release.yml`.

- [ ] **Step 4: Commit**

```bash
git add Makefile .gitignore README.md .github/
git commit -m "chore(release): configure cross-compilation build targets and release workflow"
```
