# agy-switch

Multi-account manager for [Antigravity CLI](https://github.com/google/antigravity) (`agy`). Switch Google accounts, view Gemini and Claude quotas, and route `agy` through a managed proxy.

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

Existing live credentials are saved before starting a new login. Profile names default to the email prefix; a conflicting name gets a numeric suffix. Logging in again with the same OAuth grant updates its existing profile.

In the account picker: `↑`/`↓` navigate, `Enter` switches and launches `agy`, `r` refreshes quotas, and `q` quits. Cached quotas appear immediately while fresh values load in the background. Cached values remain visible if a refresh fails.

Quota requests share a 15-second deadline and run at most four HTTP requests at a time. Adjust these with `--quota-timeout` and `--quota-concurrency`, for example `agy-switch --quota-timeout 30s --quota-concurrency 8`. Closing the picker cancels its pending requests.

## Commands

| Command | Behavior |
| --- | --- |
| `agy-switch` / `agy-switch list` | Interactive account picker |
| `agy-switch ls` | Plain account list |
| `agy-switch login [name]` | Save and activate an account |
| `agy-switch use <name>` | Switch accounts without launching agy |
| `agy-switch rename <old> <new>` | Rename a profile |
| `agy-switch logout [name]` | Remove a local profile; clear the live login if it matches |
| `agy-switch proxy` | Interactive proxy selector |
| `agy-switch proxy add <name> --server <host> --port <port>` | Register a proxy |
| `agy-switch proxy on [name]` / `off` | Enable a configured proxy / disable routing |
| `agy-switch proxy status` | Show the active proxy and probe its exit country |
| `agy-switch bridge status` / `stop` | Inspect or stop the local SOCKS bridge |
| `agy-switch --version` | Print the version |

## Proxies

Add a proxy with automatic protocol detection, then enable it:

```sh
agy-switch proxy add work --server proxy.example.com --port 1080
agy-switch proxy on work
```

Use `--protocol http`, `https`, `socks5`, or `socks5h` to select a known protocol without probing. For authentication, use `--username <user>` and `--password-env <variable-name>` to read the password from an environment variable. Add `--activate` to enable a proxy immediately after registration. Existing proxy names cannot be overwritten by `proxy add`.

In the proxy selector: `↑`/`↓` navigate, `space` toggles the selected proxy, and `Enter` or `q` exits. Register proxies with `proxy add` before opening the selector.

Enabling a proxy installs an `agy` shim in `~/.local/bin` on Unix or `%LOCALAPPDATA%\agy-switch\bin` on Windows. Put that directory before the original agy directory on `PATH`. If an original executable occupies the shim path, it is moved to `agy.real` (or `agy.real.exe`). Disabling routing restores it. A pre-existing backup is never overwritten.

SOCKS proxies use a separate local HTTP bridge so agy and its child tools can use standard HTTP proxy environment variables. The bridge binds to localhost, requires a random authentication token, survives the session that started it, and exits after five idle minutes with no agy processes running. `bridge stop` sends an authenticated stop request to the bridge. Bridge logs are stored in the data directory as `bridge.log`.

## Data and credentials

Data is stored under `$XDG_DATA_HOME/agy-switch` (default `~/.local/share/agy-switch`) on Unix and `%LOCALAPPDATA%\agy-switch` on Windows. Profiles contain OAuth credentials and are written with private file permissions.

Live credentials use the macOS Keychain, Windows Credential Manager, or agy's token files in `~/.gemini`, according to the platform. Profile switching restores the selected credential to agy's live store.

Account changes are serialized across agy-switch processes. Failed changes attempt to restore the previous credentials and profile files. An incomplete recovery returns an error and retains its recovery journal. If a command is interrupted, the next account operation attempts recovery before making another change.

### OAuth client configuration

Refreshing an expired quota access token requires the OAuth client associated with its refresh token. Configure `AGY_SWITCH_OAUTH_CLIENT_ID` and `AGY_SWITCH_OAUTH_CLIENT_SECRET`, or place an `oauth_client.json` file in the data directory:

```json
{"client_id":"your-client-id","client_secret":"your-client-secret"}
```

Set both environment variables together; they override the file. On Unix, keep the file readable only by its owner (`chmod 600`). A different OAuth client's credentials cannot refresh an existing token. OAuth client credentials are no longer embedded in the source or distributed binaries. Account switching and cached quota display work without this configuration; quota refresh reports a configuration error when an access token needs renewal.

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
