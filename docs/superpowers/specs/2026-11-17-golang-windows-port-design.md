# Design Document: Golang Port & Windows Support for agy-switch

**Date:** 2026-11-17  
**Status:** Approved by Builder Review (Pending User Approval)  
**Target:** agy-switch (asw) Go Rewrite & Cross-Platform Support

---

## 1. Intent & Context

`agy-switch` is currently a single ~2,900-line Python script (`bin/agy-switch`) wrapped in a Node.js launcher (`lib/launcher.js`). It manages multiple Google accounts for the Antigravity CLI (`agy`), displays live Claude/Gemini quota statuses, and provides an integrated proxy manager with a local SOCKS5-to-HTTP bridge.

Currently, it cannot run on Windows because:
1. The interactive TUI relies on Unix-specific modules (`termios`, `tty`).
2. SOCKS bridge synchronization relies on Unix `fcntl.flock`.
3. The proxy interception shim is a bash script (`~/.local/bin/agy`).
4. Path resolution and credential handling assume Unix filesystem layouts and macOS Keychain (`security` CLI).
5. It requires Python 3.7+ and Node.js pre-installed on the host.

This project will port `agy-switch` entirely to Go, producing standalone, statically linked binaries (`agy-switch` / `asw` and `agy-switch.exe` / `asw.exe`) with zero runtime dependencies. Distribution will focus on direct binary releases (GitHub Releases, Scoop, Homebrew, install script).

---

## 2. Goals, Non-Goals, and Anti-Goals

### Goals
- **100% Feature Parity:**
  - **Account Profile Management:** Snapshot, restore, list, rename, logout, OAuth revoke, and `login` (stash existing token -> launch real agy browser login -> detect new token -> snapshot profile).
  - **Quota Display & Polling:** Live fetch and caching for Gemini/Claude 5h & weekly limits.
  - **Interactive Pickers:** Keyboard-navigable TUI for switching accounts and managing proxies.
  - **Proxy Management:** Add, edit, delete, toggle proxy; auto-probe protocol (`https`, `socks5h`, `http`) and exit-IP country code (`api.country.is`).
  - **SOCKS Bridge:** Background daemon proxying HTTP to SOCKS5 with remote DNS resolution and random token authentication.
  - **Transparent Shim:** Auto-inject proxy environment variables when running `agy`.
- **First-Class Windows Support:**
  - Works in Windows Terminal, PowerShell, and `cmd.exe`.
  - Windows path handling (`%LOCALAPPDATA%\agy-switch`, `%USERPROFILE%\.gemini\...`).
  - Windows Credential Manager integration for OAuth tokens with fallback to file.
  - Dedicated Windows binary shim (`agy.exe`) in PATH to guarantee precedence over real `agy.exe`.
- **Single Native Binary Distribution:**
  - Zero Python / Node.js dependency.
  - Easy cross-compilation with `CGO_ENABLED=0`.

### Non-Goals
- Maintaining the npm package as the primary distribution channel (it can optionally wrap the Go binary in the future, but direct binary releases take priority).
- Redesigning the underlying OAuth protocols or proxy storage JSON format (existing profiles in `~/.local/share/agy-switch` must remain compatible).

### Anti-Goals
- Adding heavy CGO dependencies that require cross-compilers (e.g. GCC/MinGW) for Windows/macOS/Linux targets.
- Sacrificing the interactive TUI experience for a dumb CLI prompt.

---

## 3. Approaches Considered

### Approach 1: Cobra + Bubble Tea / Lipgloss (Chosen)
- **CLI Framework:** `github.com/spf13/cobra` for subcommands and flags.
- **TUI Framework:** `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/lipgloss`.
- **Keyring:** Pure-Go credential management using Win32 API (`x/sys/windows`), macOS Keychain CLI, and file fallback.
- **Why Chosen:** Bubble Tea provides robust cross-platform terminal raw mode handling across Windows Terminal, PowerShell, cmd.exe, and Unix ptys, with clean state-driven UI rendering and zero CGO.

### Approach 2: Standard Library + `golang.org/x/term` (Rejected)
- Roll custom ANSI renderers and raw mode loops.
- **Reason for Rejection:** High risk of Windows-specific escape code and key event handling bugs (arrow keys, resize events, VT100 edge cases in cmd.exe).

### Approach 3: CLI-only Without TUI (Rejected)
- Non-interactive commands only, delegating selection to external tools like `fzf`.
- **Reason for Rejection:** Regresses the core value proposition of `asw` (one-keystroke account switching with live quota dashboard).

---

## 4. Architecture & Component Design

### 4.1 Project Layout
```
agy-switch/
├── cmd/
│   ├── agy-switch/
│   │   └── main.go       # Main entry point (Cobra root command)
│   ├── asw/
│   │   └── main.go       # Alias entry point (defaults to interactive picker)
│   └── agy-shim/
│       └── main.go       # Lightweight binary shim (or multi-call entrypoint)
├── pkg/
│   ├── config/           # Cross-platform paths and environment resolution
│   ├── profile/          # Account profile models, JSON stores, switching logic, login
│   ├── keyring/          # Windows Credential Manager, macOS Keychain, file token ops
│   ├── quota/            # Google AI quota API client, OAuth client discovery, cache
│   ├── proxy/            # Proxy store, protocol racer, geo/IP probe
│   ├── bridge/           # Local HTTP-to-SOCKS5 proxy server & cross-platform lock
│   ├── shim/             # agy interceptor (Unix bash shim / Windows agy.exe binary shim)
│   └── tui/              # Bubble Tea models (PickerModel, ProxyModel)
├── go.mod
├── go.sum
└── Makefile
```

### 4.2 Cross-Platform Path Strategy (`pkg/config`)
- **Data / Config Directory:**
  - Unix: `$XDG_DATA_HOME/agy-switch` (default `~/.local/share/agy-switch`)
  - Windows: `%LOCALAPPDATA%\agy-switch`
- **Binary / Shim Directory:**
  - Unix: `~/.local/bin/agy` (bash script shim)
  - Windows: `%LOCALAPPDATA%\agy-switch\bin\agy.exe` (prefixed to User PATH)
- **Antigravity Live Token:**
  - Unix: `~/.gemini/antigravity-cli/antigravity-oauth-token` (and legacy `~/.gemini/oauth_creds.json`)
  - Windows: `%USERPROFILE%\.gemini\antigravity-cli\antigravity-oauth-token` + Windows Credential Manager (`service="gemini", account="antigravity"`).

### 4.3 Keyring & Credential Store (`pkg/keyring`)
`agy` uses `go-keyring`. On macOS with a GUI session, tokens are stored in the Keychain; on Windows, `go-keyring` writes to Windows Credential Manager.
- Porting strategy:
  1. Detect OS. On Windows, use Win32 `CredReadW` / `CredWriteW` / `CredDeleteW` via `golang.org/x/sys/windows` (pure Go, no CGO).
  2. On macOS, use `/usr/bin/security` command runner (same as Python `_kc_run`).
  3. If keyring read fails or on Linux/SSH, read and write the token file directly.
  4. Ensure profile snapshots store tokens safely with `0600` permissions.

### 4.4 SOCKS Bridge & Concurrency (`pkg/bridge`)
- **Locking:** `gofrs/flock` for cross-platform file locking (`bridge.lock`).
- **Proxy Server:** Standard `net/http` reverse/forward proxy, with SOCKS5 dialing via `golang.org/x/net/proxy`.
- **Security:** Local authentication token validation via HTTP `Proxy-Authorization` header or Basic auth.
- **Process Management & Lifecycle (Windows & Unix):**
  - Background daemon start on Windows uses `windows.CREATE_NO_WINDOW` / `windows.DETACHED_PROCESS` to prevent any terminal popup window.
  - Idle monitoring: Checks running `agy` processes periodically. On Windows, uses `windows.CreateToolhelp32Snapshot` to enumerate processes named `agy.exe` (avoiding external CLI calls like `ps`/`pgrep`). Bridge cleanly stops when idle for 5 minutes with zero running `agy` processes.

### 4.5 Transparent Shim Strategy (`pkg/shim`)
- **Unix:**
  - Generates `~/.local/bin/agy` bash script pointing to real `agy` with proxy environment exported.
- **Windows:**
  - To prevent `.cmd` vs `.exe` priority issues and PowerShell launch delays, compile a dedicated binary shim `agy.exe` (or multi-call `asw` configured as `agy.exe`) into `%LOCALAPPDATA%\agy-switch\bin\agy.exe`.
  - When proxy is enabled, `agy-switch` ensures `%LOCALAPPDATA%\agy-switch\bin` is prepended to the user's `PATH`.
  - When executed, `agy.exe` loads proxy configuration, ensures the bridge daemon is running if SOCKS is active, injects `HTTP_PROXY`/`HTTPS_PROXY` env vars, and transparently executes the real `agy.exe` forwarding standard I/O and exit codes.

### 4.6 OAuth Client Discovery & Quota Polling (`pkg/quota`)
- Google AI Cloud Code APIs require OAuth client credentials.
- In Go:
  - Embed known Google Cloud Code client ID / secret pairs directly in code as fast-path defaults.
  - If dynamic extraction is necessary, provide a fast streaming byte scanner for `agy` / `agy.exe` (matching regex `[0-9]+-[a-z0-9_]+\.apps\.googleusercontent\.com`).
  - Cache the validated pair in `oauth_client.json`.
- Fetch quota data via HTTP client with exponential backoff and cache with TTL (15 min cache).

### 4.7 Terminal UI (`pkg/tui`)
- Implemented with `charmbracelet/bubbletea`.
- **Account Picker:**
  - Displays profiles, email, subscription tier, Gemini/Claude quotas, and last-used timestamp.
  - Keys: `↑`/`↓` navigate, `Enter` switch profile and launch `agy`, `r` reload quotas, `q`/`Esc` exit.
- **Proxy Manager:**
  - Displays configured proxies, active status, ping/protocol, geo location, IP type.
  - Keys: `↑`/`↓` navigate, `Space` toggle active, `Enter` save & exit, `a` add, `e` edit, `d` delete, `r` re-probe, `q` quit.

---

## 5. CLI Commands

Full parity with Python version:
- `asw` / `agy-switch` (interactive account picker)
- `asw list` (same as above)
- `asw ls` (plain, non-interactive account list)
- `asw login` (stash current token -> invoke real `agy` login -> snapshot new token)
- `asw use <name>` (switch active profile)
- `asw rename <old> <new>` (rename profile)
- `asw logout [name] [--revoke]` (delete profile, optionally revoke OAuth grant)
- `asw proxy [on|off|status]` (interactive or non-interactive proxy manager)
- `asw bridge [status|stop]` (inspect or terminate SOCKS bridge)
- `asw version` / `--version` (print build version)

---

## 6. Implementation & Verification Plan

1. **Phase 1: Project Setup & Core Models (`pkg/config`, `pkg/profile`, `pkg/keyring`)**
   - Initialize Go module `github.com/ZhangNy301/agy-switch` (or local module).
   - Implement cross-platform paths, JSON profile storage, token parsing, and credential store (WinCred / Keychain / File).
   - Implement CLI commands: `ls`, `use`, `rename`, `logout`.
2. **Phase 2: Quota & OAuth Client Discovery (`pkg/quota`)**
   - Implement token refresh and quota API query.
   - Implement background quota refresh and caching.
3. **Phase 3: Interactive TUI (`pkg/tui`)**
   - Bubble Tea models for account picker with live quota rendering.
   - Bubble Tea models for proxy manager.
4. **Phase 4: Proxy Manager, Protocol Racing & Geo Probe (`pkg/proxy`)**
   - TCP/TLS/HTTP/SOCKS5 racer for proxy detection.
   - GeoIP probe via `api.country.is` with reachability check.
5. **Phase 5: SOCKS Bridge & Transparent Shim (`pkg/bridge`, `pkg/shim`)**
   - SOCKS5 to HTTP proxy bridge with random token auth and idle timeout.
   - Cross-platform file lock via `gofrs/flock`.
   - Windows process snapshot using `CreateToolhelp32Snapshot`.
   - Windows `agy.exe` shim and Unix bash shim generation.
6. **Phase 6: Multi-Platform Build & Verification**
   - Build and test locally on Linux (`go test ./...`).
   - Cross-compile for Windows (`GOOS=windows GOARCH=amd64 go build ...`) and macOS (`GOOS=darwin`).
   - Setup GitHub Actions release workflow with GoReleaser.
