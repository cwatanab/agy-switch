//go:build windows

package keyring

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"golang.org/x/sys/windows"
)

const (
	targetName              = "gemini:antigravity"
	userName                = "antigravity"
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	goKeyringPrefix         = "go-keyring-base64:"
)

var (
	advapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     uintptr
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type WindowsKeyringStore struct {
	fallback *FileStore
}

func platformStore(paths *config.Paths) TokenStore {
	return &WindowsKeyringStore{
		fallback: NewFileStore(paths),
	}
}

func (w *WindowsKeyringStore) ReadLive() ([]byte, error) {
	targetPtr, err := syscall.UTF16PtrFromString(targetName)
	if err == nil {
		var credPtr *credential
		r1, _, _ := procCredReadW.Call(
			uintptr(unsafe.Pointer(targetPtr)),
			uintptr(credTypeGeneric),
			0,
			uintptr(unsafe.Pointer(&credPtr)),
		)
		if r1 != 0 && credPtr != nil {
			defer procCredFree.Call(uintptr(unsafe.Pointer(credPtr)))
			rawBytes := unsafe.Slice((*byte)(unsafe.Pointer(credPtr.CredentialBlob)), credPtr.CredentialBlobSize)
			raw := string(rawBytes)
			// Backward compatibility: if previous buggy version wrote base64 prefix
			if strings.HasPrefix(raw, goKeyringPrefix) {
				b64 := strings.TrimPrefix(raw, goKeyringPrefix)
				if decoded, bErr := base64.StdEncoding.DecodeString(b64); bErr == nil && len(decoded) > 0 {
					return decoded, nil
				}
			}
			// Copy bytes out of C-allocated memory
			res := make([]byte, len(rawBytes))
			copy(res, rawBytes)
			return res, nil
		}
	}
	return w.fallback.ReadLive()
}

func (w *WindowsKeyringStore) WriteLive(data []byte) error {
	var credErr error
	targetPtr, err := syscall.UTF16PtrFromString(targetName)
	userPtr, uErr := syscall.UTF16PtrFromString(userName)
	if err == nil && uErr == nil && len(data) > 0 {
		cred := credential{
			Flags:              0,
			Type:               credTypeGeneric,
			TargetName:         targetPtr,
			Comment:            nil,
			CredentialBlobSize: uint32(len(data)),
			CredentialBlob:     uintptr(unsafe.Pointer(&data[0])),
			Persist:            credPersistLocalMachine,
			UserName:           userPtr,
		}
		r1, _, callErr := procCredWriteW.Call(
			uintptr(unsafe.Pointer(&cred)),
			0,
		)
		if r1 == 0 {
			credErr = fmt.Errorf("CredWriteW failed: %v", callErr)
		}
	}

	// Always keep the mirror token file in sync
	fallbackErr := w.fallback.WriteLive(data)
	return verifyWrite(w, data, credErr, fallbackErr)
}

func (w *WindowsKeyringStore) DeleteLive() error {
	var credErr error
	targetPtr, err := syscall.UTF16PtrFromString(targetName)
	if err == nil {
		result, _, callErr := procCredDeleteW.Call(
			uintptr(unsafe.Pointer(targetPtr)),
			uintptr(credTypeGeneric),
			0,
		)
		if result == 0 && !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			credErr = fmt.Errorf("delete Windows credential: %w", callErr)
		}
	}
	return errors.Join(credErr, w.fallback.DeleteLive())
}
