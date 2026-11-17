package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

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

func ResolvePaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home directory: %w", err)
	}

	var dataDir, binDir, proxyEnv string
	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		dataDir = filepath.Join(localAppData, "agy-switch")
		binDir = filepath.Join(dataDir, "bin")
		proxyEnv = filepath.Join(dataDir, "proxy.bat")
	} else {
		xdgData := os.Getenv("XDG_DATA_HOME")
		if xdgData == "" {
			xdgData = filepath.Join(home, ".local", "share")
		}
		dataDir = filepath.Join(xdgData, "agy-switch")
		binDir = filepath.Join(home, ".local", "bin")
		proxyEnv = filepath.Join(dataDir, "proxy.env")
	}

	return &Paths{
		HomeDir:     home,
		DataDir:     dataDir,
		ProfilesDir: filepath.Join(dataDir, "profiles"),
		LiveToken:   filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"),
		LegacyToken: filepath.Join(home, ".gemini", "oauth_creds.json"),
		BinDir:      binDir,
		ProxyEnv:    proxyEnv,
		BridgeLock:  filepath.Join(dataDir, "bridge.lock"),
		BridgeLog:   filepath.Join(dataDir, "bridge.log"),
	}, nil
}

func EnsureDirs(p *Paths) error {
	for _, dir := range []string{p.DataDir, p.ProfilesDir, p.BinDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("ensure directory %s: %w", dir, err)
		}
	}
	return nil
}
