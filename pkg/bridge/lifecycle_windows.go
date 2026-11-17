//go:build windows

package bridge

import (
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func CountAgyProcesses() (int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	err = windows.Process32First(snapshot, &entry)
	if err != nil {
		return 0, err
	}

	count := 0
	for {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		nameLower := strings.ToLower(name)
		if entry.ProcessID != uint32(os.Getpid()) && (nameLower == "agy.exe" || nameLower == "agy.real.exe" || nameLower == "antigravity.exe") {
			count++
		}

		err = windows.Process32Next(snapshot, &entry)
		if err != nil {
			break
		}
	}

	return count, nil
}
