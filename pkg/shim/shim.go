package shim

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

const ShimMarker = "agy-switch proxy shim"

func IsShim(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// Native Windows shims contain the marker in their executable's string table.
	buf := make([]byte, 64*1024+len(ShimMarker)-1)
	carry := 0
	for {
		n, err := f.Read(buf[carry:])
		total := carry + n
		if bytes.Contains(buf[:total], []byte(ShimMarker)) {
			return true
		}
		if err != nil {
			return false
		}
		carry = min(total, len(ShimMarker)-1)
		copy(buf[:carry], buf[total-carry:total])
	}
}

func backupPath(path string) string {
	ext := filepath.Ext(path)
	if ext == ".exe" || ext == ".cmd" || ext == ".bat" {
		return strings.TrimSuffix(path, ext) + ".real" + ext
	}
	return path + ".real"
}

func FindRealAgy(paths *config.Paths) (string, error) {
	if custom := os.Getenv("AGY_BIN"); custom != "" {
		if resolved, err := exec.LookPath(custom); err == nil && !IsShim(resolved) {
			return resolved, nil
		}
	}

	binNames := []string{"agy"}
	if runtime.GOOS == "windows" {
		binNames = []string{"agy.exe", "agy.cmd", "agy.bat"}
	}

	selfExe, _ := os.Executable()
	var selfInfo os.FileInfo
	if selfExe != "" {
		selfInfo, _ = os.Stat(selfExe)
	}

	// A real executable originally in our shim directory is moved aside on install.
	for _, name := range binNames {
		candidate := backupPath(filepath.Join(paths.BinDir, name))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && !IsShim(candidate) {
			return candidate, nil
		}
	}
	pathEnv := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathEnv) {
		cleanDir := filepath.Clean(dir)
		for _, name := range binNames {
			candidate := filepath.Join(cleanDir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				if selfInfo != nil && os.SameFile(selfInfo, info) {
					continue // Avoid self-execution loop
				}
				if !IsShim(candidate) {
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("real agy binary not found on PATH")
}

func Install(paths *config.Paths, realAgy string) error {
	if err := config.EnsureDirs(paths); err != nil {
		return err
	}

	selfExe, err := os.Executable()
	if err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(selfExe)
		if err != nil {
			return err
		}
		return installFile(filepath.Join(paths.BinDir, "agy.exe"), data)
	}

	quotedExe := "'" + strings.ReplaceAll(selfExe, "'", "'\\''") + "'"
	shContent := fmt.Sprintf("#!/bin/sh\n# %s\nexec %s shim-exec \"$@\"\n", ShimMarker, quotedExe)
	shPath := filepath.Join(paths.BinDir, "agy")
	return installFile(shPath, []byte(shContent))
}

func installFile(path string, data []byte) error {
	backedUp := false
	if _, err := os.Lstat(path); err == nil && !IsShim(path) {
		if _, err := os.Lstat(backupPath(path)); !os.IsNotExist(err) {
			return fmt.Errorf("refusing to replace %s: backup already exists", path)
		}
		if err := os.Rename(path, backupPath(path)); err != nil {
			return fmt.Errorf("back up existing agy: %w", err)
		}
		backedUp = true
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := util.WriteFileAtomic(path, data, 0755); err != nil {
		if backedUp {
			if restoreErr := os.Rename(backupPath(path), path); restoreErr != nil {
				return fmt.Errorf("install shim: %v; restore original from %s: %w", err, backupPath(path), restoreErr)
			}
		}
		return fmt.Errorf("write agy shim: %w", err)
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func Uninstall(paths *config.Paths) error {
	candidates := []string{
		filepath.Join(paths.BinDir, "agy"),
		filepath.Join(paths.BinDir, "agy.cmd"),
		filepath.Join(paths.BinDir, "agy.exe"),
	}

	for _, c := range candidates {
		if _, err := os.Lstat(c); err == nil {
			if IsShim(c) {
				if err := os.Remove(c); err != nil {
					return err
				}
			} else {
				continue
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := os.Lstat(backupPath(c)); err == nil {
			if err := os.Rename(backupPath(c), c); err != nil {
				return fmt.Errorf("restore original agy: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
