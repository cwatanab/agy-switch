# agy-switch

Multi-account manager for [Antigravity CLI](https://github.com/google/antigravity) (`agy`). Switch Google accounts, view Gemini and Claude 5-hour and weekly quotas, and route `agy` through managed proxies.

This project is a Go implementation based on [ZhangNy301/agy-switch](https://github.com/ZhangNy301/agy-switch).

## Requirements

- Windows, Linux, or macOS, on amd64 or arm64.
- Antigravity CLI installed and available on `PATH`.
- The standalone binary requires no Python or Node.js runtime.

## Install

Download the binary for your platform from [GitHub Releases](https://github.com/cwatanab/agy-switch/releases):

| Platform | File |
| --- | --- |
| Windows x64 | `agy-switch-windows-amd64.exe` |
| Windows ARM64 | `agy-switch-windows-arm64.exe` |
| Linux x64 | `agy-switch-linux-amd64` |
| Linux ARM64 | `agy-switch-linux-arm64` |
| macOS Intel | `agy-switch-darwin-amd64` |
| macOS Apple Silicon | `agy-switch-darwin-arm64` |

On Windows, rename the file to `agy-switch.exe` and place it in a directory on your `PATH`. On Linux and macOS, rename it to `agy-switch`, run `chmod +x agy-switch`, and place it in a directory on your `PATH`, such as `~/.local/bin`.

The current version is distributed as a Go binary. The former Python installer, npm launcher, and `asw` alias are no longer distributed.

## Quick start

```sh
agy-switch login        # save an account through agy's browser login
agy-switch login        # add another account
agy-switch              # select an account and launch agy
```

Existing live credentials are saved before starting a new login, and an existing orphan login is adopted automatically on first run (with an OAuth token refresh to verify identity). Profile names default to the email prefix (with an interactive prompt on TTY when no name argument is given); a conflicting name gets a numeric suffix. Logging in again with the same OAuth grant updates its existing profile.

In the account picker: `↑`/`↓` navigate (starting on the currently active account), `Enter` switches and launches `agy`, `r` refreshes quotas, and `q` quits. Each account shows its active indicator (`●`), subscription tier (`Free` / `Pro` / `Ultra`), last-active time, and Gemini / Claude 5-hour and weekly quotas with reset countdowns (`↻4d21h`) or `disabled` state. Cached quotas appear immediately while fresh values load in the background. Cached values remain visible if a refresh fails.

Switching accounts (`agy-switch` or `agy-switch use`) warns if another `agy` process is still running, and `agy-switch ls` warns if a running `agy` overwrote the live token (which the next `agy` start automatically re-asserts). Startup also performs a cached pre-flight network check (`.netcheck`) and warns if Google APIs are unreachable or your exit IP region is unsupported by Google AI.

Quota requests share a 15-second deadline and run at most four HTTP requests at a time. Adjust these with `--quota-timeout` and `--quota-concurrency`, for example `agy-switch --quota-timeout 30s --quota-concurrency 8`. Closing the picker cancels its pending requests.

## Commands

| Command | Behavior |
| --- | --- |
| `agy-switch` / `agy-switch list` | Interactive account picker |
| `agy-switch ls [-r]` | Plain account list (`-r`/`--refresh` forces quota reload) |
| `agy-switch login [name]` | Save and activate an account (includes network pre-check) |
| `agy-switch use <name>` | Switch accounts without launching agy |
| `agy-switch rename <old> <new>` | Rename a profile |
| `agy-switch logout [name] [--revoke]` | Remove a local profile (`--revoke` also revokes the OAuth grant at Google) |
| `agy-switch proxy` | Interactive proxy manager (prints status when non-interactive) |
| `agy-switch proxy add <name> --server <host> --port <port>` | Register a proxy (auto-detects protocol unless `--protocol` is given) |
| `agy-switch proxy edit <name>` | Edit fields of an existing proxy (`--server`, `--port`, `--protocol`, `--username`, `--password-env`, `--validity`, `--fallback`) |
| `agy-switch proxy rename <old> <new>` | Rename a configured proxy |
| `agy-switch proxy delete <name>` | Remove a configured proxy |
| `agy-switch proxy on [name]` / `off` | Enable a configured proxy / disable routing |
| `agy-switch proxy status` | Show all configured proxies, exit geo/IP-type probes, shim/bridge status, and network check |
| `agy-switch bridge status` / `stop` | Inspect or stop the local SOCKS bridge |
| `agy-switch --version` | Print the version |

## Proxies

Add a proxy with automatic protocol detection (`https`, `socks5h`, `http`), optional expiry, and direct fallback:

```sh
agy-switch proxy add work --server proxy.example.com --port 1080 --validity 30d --fallback
agy-switch proxy on work
```

Use `--protocol http`, `https`, `socks5`, or `socks5h` to select a known protocol without probing. For authentication, use `--username <user>` and `--password-env <variable-name>` to read the password from an environment variable. Add `--activate` to enable a proxy immediately after registration. Existing proxy names cannot be overwritten by `proxy add`.

In the interactive proxy manager (`agy-switch proxy`):

- `↑`/`↓` navigate · `Enter` enable & quit · `space` toggle on/off · `a` add wizard (prefills from `HTTPS_PROXY` when present and auto-detects protocol) · `e` edit fields · `R` rename · `d` delete · `r` re-probe exit IP/region/ISP/type · `q` quit

Enabling a proxy installs an `agy` shim in `~/.local/bin` on Unix or `%LOCALAPPDATA%\agy-switch\bin` on Windows. Put that directory before the original agy directory on `PATH`. If an original executable occupies the shim path, it is moved to `agy.real` (or `agy.real.exe`). Disabling routing restores it, and subsequent `agy-switch` runs automatically repair the shim if `agy update` overwrites it. Pre-existing `HTTP_PROXY`/`HTTPS_PROXY` variables already exported in your shell still take precedence, and `agy-switch` itself routes quota, OAuth, and revoke requests through the active managed proxy.

SOCKS proxies use a separate local HTTP bridge so agy and its child tools can use standard HTTP proxy environment variables (falling back to the raw `socks5h://` URL with a warning if the bridge fails to start). The bridge binds to localhost, requires a random authentication token, survives the session that started it, and exits after five idle minutes with no agy processes running. `bridge stop` sends an authenticated stop request to the bridge. Bridge logs are stored in the data directory as `bridge.log`.

## Data and credentials

Data is stored under `$XDG_DATA_HOME/agy-switch` (default `~/.local/share/agy-switch`) on Unix and `%LOCALAPPDATA%\agy-switch` on Windows. Profiles contain OAuth credentials and are written with private file permissions.

Live credentials use the macOS Keychain, Windows Credential Manager, or agy's token files in `~/.gemini`, according to the platform. Profile switching restores the selected credential to agy's live store.

Account changes are serialized across agy-switch processes. Failed changes attempt to restore the previous credentials and profile files. An incomplete recovery returns an error and retains its recovery journal. If a command is interrupted, the next account operation attempts recovery before making another change.

### OAuth client configuration

Refreshing an expired quota access token requires the OAuth client associated with its refresh token. When neither environment variables nor `oauth_client.json` are configured, `agy-switch` automatically scans the local `agy` binary to discover the OAuth client credentials and caches the verified pair in `oauth_client.json` (with `0600` permissions on Unix).

To override automatic discovery, configure `AGY_SWITCH_OAUTH_CLIENT_ID` and `AGY_SWITCH_OAUTH_CLIENT_SECRET`, or place an `oauth_client.json` file in the data directory:

```json
{"client_id":"your-client-id","client_secret":"your-client-secret"}
```

Set both environment variables together; they override the file and automatic discovery. On Unix, keep the file readable only by its owner (`chmod 600`). A different OAuth client's credentials cannot refresh an existing token. OAuth client credentials are not embedded in the source or distributed binaries. Account switching and cached quota display work without OAuth credentials; quota refresh reports an error if discovery fails or credentials are invalid.

## Build and test

Use the Go version specified in `go.mod`:

```sh
go test -race ./...
make build
make build-all
```

`make build` writes `dist/agy-switch`. `make build-all` builds all six release targets. Override the embedded version with `make build-all VERSION=1.2.3`.

## Uninstall

Run `agy-switch proxy off` to restore any original agy executable, then `agy-switch bridge stop`. Remove the `agy-switch` binary from your `PATH`. Delete the data directory only if you also want to remove saved profiles and proxy credentials.

## Acknowledgments

This project is based on and references [ZhangNy301/agy-switch](https://github.com/ZhangNy301/agy-switch).

## License

[MIT](LICENSE)
