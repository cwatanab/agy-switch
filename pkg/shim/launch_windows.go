//go:build windows

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func buildLaunchCommandLine(realAgy string, args []string) (string, string) {
	ext := strings.ToLower(filepath.Ext(realAgy))
	allArgs := append([]string{realAgy}, args...)
	if ext == ".cmd" || ext == ".bat" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		inner := windows.ComposeCommandLine(allArgs)
		cmdLine := windows.ComposeCommandLine([]string{comspec, "/d", "/c"}) + ` "` + inner + `"`
		return comspec, cmdLine
	}
	return realAgy, windows.ComposeCommandLine(allArgs)
}

func createWindowsEnvBlock(env []string) []uint16 {
	type envEntry struct {
		upperKey string
		raw      string
	}
	entries := make([]envEntry, 0, len(env))
	for _, kv := range env {
		if kv == "" || strings.IndexByte(kv, 0) >= 0 {
			continue
		}
		key, _, _ := strings.Cut(kv, "=")
		entries = append(entries, envEntry{
			upperKey: strings.ToUpper(key),
			raw:      kv,
		})
	}
	if len(entries) == 0 {
		return []uint16{0, 0}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].upperKey < entries[j].upperKey
	})
	var block []uint16
	for _, e := range entries {
		block = append(block, utf16.Encode([]rune(e.raw))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block
}

func startRealAgyProcess(realAgy string, args []string, env []string) error {
	appName, cmdLine := buildLaunchCommandLine(realAgy, args)
	appPtr, err := windows.UTF16PtrFromString(appName)
	if err != nil {
		return fmt.Errorf("encode executable path: %w", err)
	}
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return fmt.Errorf("encode command line: %w", err)
	}
	envBlock := createWindowsEnvBlock(env)

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))

	flags := uint32(windows.CREATE_NEW_CONSOLE | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_UNICODE_ENVIRONMENT)
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		appPtr,
		cmdLinePtr,
		nil,
		nil,
		false,
		flags,
		&envBlock[0],
		nil,
		&si,
		&pi,
	)
	runtime.KeepAlive(appPtr)
	runtime.KeepAlive(cmdLinePtr)
	runtime.KeepAlive(envBlock)
	runtime.KeepAlive(&si)
	if err != nil {
		return fmt.Errorf("start %s: %w", realAgy, err)
	}

	_ = windows.CloseHandle(pi.Thread)
	_ = windows.CloseHandle(pi.Process)
	return nil
}
