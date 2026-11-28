//go:build windows

package quota

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	errHiddenDesktopUnavailable = errors.New("hidden desktop unavailable")
	errConPTYUnavailable        = errors.New("conpty unavailable")
	kernel32DLL                 = windows.NewLazySystemDLL("kernel32.dll")
	procUpdateProcThreadAttrPtr = kernel32DLL.NewProc("UpdateProcThreadAttribute")
	user32DLL                   = windows.NewLazySystemDLL("user32.dll")
	procCreateDesktop           = user32DLL.NewProc("CreateDesktopW")
	procCloseDesktop            = user32DLL.NewProc("CloseDesktop")
	hiddenDesktopSeq            atomic.Uint64
)

func execUsageCommand(ctx context.Context, agyBin string, args []string, env []string) ([]byte, []byte, error) {
	stdout, stderr, err := execUsageCommandHiddenDesktop(ctx, agyBin, args, env)
	if !errors.Is(err, errHiddenDesktopUnavailable) {
		return stdout, stderr, err
	}
	stdout, stderr, err = execUsageCommandConPTY(ctx, agyBin, args, env)
	if errors.Is(err, errConPTYUnavailable) {
		return execUsageCommandFallback(ctx, agyBin, args, env)
	}
	return stdout, stderr, err
}

func execUsageCommandFallback(ctx context.Context, agyBin string, args []string, env []string) ([]byte, []byte, error) {
	const createNoWindow = 0x08000000
	cmd := exec.CommandContext(ctx, agyBin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func buildWindowsCommandLine(agyBin string, args []string) (string, string) {
	ext := strings.ToLower(filepath.Ext(agyBin))
	allArgs := append([]string{agyBin}, args...)
	if ext == ".cmd" || ext == ".bat" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		inner := windows.ComposeCommandLine(allArgs)
		cmdLine := windows.ComposeCommandLine([]string{comspec, "/d", "/c"}) + ` "` + inner + `"`
		return comspec, cmdLine
	}
	return agyBin, windows.ComposeCommandLine(allArgs)
}

func createEnvBlock(env []string) []uint16 {
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

func createHiddenDesktop() (windows.Handle, string, error) {
	const hiddenDesktopAccess = 0x0001 | 0x0002 | 0x0004 | 0x0040 | 0x0080 | 0x0100 | 0x00010000
	name := fmt.Sprintf("agy-switch-%d-%d", os.Getpid(), hiddenDesktopSeq.Add(1))
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, "", err
	}
	r1, _, callErr := procCreateDesktop.Call(
		uintptr(unsafe.Pointer(namePtr)),
		0,
		0,
		0,
		uintptr(hiddenDesktopAccess),
		0,
	)
	runtime.KeepAlive(namePtr)
	if r1 == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return 0, "", callErr
		}
		return 0, "", syscall.EINVAL
	}
	return windows.Handle(r1), name, nil
}

func closeHiddenDesktop(h windows.Handle) {
	if h != 0 {
		_, _, _ = procCloseDesktop.Call(uintptr(h))
	}
}

type childStdIO struct {
	nulHandle   windows.Handle
	stdoutRead  windows.Handle
	stdoutWrite windows.Handle
	stderrRead  windows.Handle
	stderrWrite windows.Handle
}

func newChildStdIO() (*childStdIO, error) {
	var stdoutRead, stdoutWrite windows.Handle
	if err := windows.CreatePipe(&stdoutRead, &stdoutWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	var stderrRead, stderrWrite windows.Handle
	if err := windows.CreatePipe(&stderrRead, &stderrWrite, nil, 0); err != nil {
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(stdoutWrite)
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	nulPath, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(stdoutWrite)
		_ = windows.CloseHandle(stderrRead)
		_ = windows.CloseHandle(stderrWrite)
		return nil, err
	}
	nulHandle, err := windows.CreateFile(
		nulPath,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		_ = windows.CloseHandle(stdoutRead)
		_ = windows.CloseHandle(stdoutWrite)
		_ = windows.CloseHandle(stderrRead)
		_ = windows.CloseHandle(stderrWrite)
		return nil, fmt.Errorf("open NUL: %w", err)
	}
	ioHandles := &childStdIO{
		nulHandle:   nulHandle,
		stdoutRead:  stdoutRead,
		stdoutWrite: stdoutWrite,
		stderrRead:  stderrRead,
		stderrWrite: stderrWrite,
	}
	for _, h := range ioHandles.inheritHandles() {
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			ioHandles.closeWriters()
			ioHandles.closeReaders()
			return nil, fmt.Errorf("set handle inherit: %w", err)
		}
	}
	return ioHandles, nil
}

func (c *childStdIO) inheritHandles() []windows.Handle {
	return []windows.Handle{c.nulHandle, c.stdoutWrite, c.stderrWrite}
}

func (c *childStdIO) closeWriters() {
	_ = windows.CloseHandle(c.nulHandle)
	_ = windows.CloseHandle(c.stdoutWrite)
	_ = windows.CloseHandle(c.stderrWrite)
}

func (c *childStdIO) closeReaders() {
	_ = windows.CloseHandle(c.stdoutRead)
	_ = windows.CloseHandle(c.stderrRead)
}

func createKillOnCloseJob() windows.Handle {
	j, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	var limit windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		j,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limit)),
		uint32(unsafe.Sizeof(limit)),
	); err != nil {
		_ = windows.CloseHandle(j)
		return 0
	}
	return j
}

func runCreatedProcess(
	ctx context.Context,
	pi *windows.ProcessInformation,
	job windows.Handle,
	ioHandles *childStdIO,
	afterJobClose func(),
) ([]byte, []byte, error) {
	if job != 0 {
		if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
			_ = windows.CloseHandle(job)
			job = 0
		}
	}

	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		ioHandles.closeReaders()
		if afterJobClose != nil {
			afterJobClose()
		}
		return nil, nil, fmt.Errorf("resume thread: %w", err)
	}

	stdoutFile := os.NewFile(uintptr(ioHandles.stdoutRead), "stdout")
	stderrFile := os.NewFile(uintptr(ioHandles.stderrRead), "stderr")
	stdoutCh := make(chan []byte, 1)
	stderrCh := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(stdoutFile)
		_ = stdoutFile.Close()
		stdoutCh <- b
	}()
	go func() {
		b, _ := io.ReadAll(stderrFile)
		_ = stderrFile.Close()
		stderrCh <- b
	}()

	waitCh := make(chan error, 1)
	go func() {
		_, wErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		waitCh <- wErr
	}()

	var waitErr error
	var canceled bool
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		canceled = true
		if job != 0 {
			_ = windows.TerminateJobObject(job, 1)
		}
		_ = windows.TerminateProcess(pi.Process, 1)
		<-waitCh
	}

	if canceled {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		<-stdoutCh
		<-stderrCh
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		if afterJobClose != nil {
			afterJobClose()
		}
		return nil, nil, ctx.Err()
	}

	var exitCode uint32
	exitErr := windows.GetExitCodeProcess(pi.Process, &exitCode)

	if job != 0 {
		_ = windows.CloseHandle(job)
	}

	stdoutBytes := <-stdoutCh
	stderrBytes := <-stderrCh

	_ = windows.CloseHandle(pi.Thread)
	_ = windows.CloseHandle(pi.Process)
	if afterJobClose != nil {
		afterJobClose()
	}

	if waitErr != nil {
		return stdoutBytes, stderrBytes, fmt.Errorf("wait process: %w", waitErr)
	}
	if exitErr != nil {
		return stdoutBytes, stderrBytes, fmt.Errorf("get exit code: %w", exitErr)
	}
	if exitCode != 0 {
		return stdoutBytes, stderrBytes, fmt.Errorf("exit status %d", exitCode)
	}
	return stdoutBytes, stderrBytes, nil
}

func execUsageCommandHiddenDesktop(ctx context.Context, agyBin string, args []string, env []string) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	hDesktop, desktopName, err := createHiddenDesktop()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errHiddenDesktopUnavailable, err)
	}
	desktopClosed := false
	closeDesktopOnce := func() {
		if !desktopClosed {
			desktopClosed = true
			closeHiddenDesktop(hDesktop)
		}
	}
	defer closeDesktopOnce()

	ioHandles, err := newChildStdIO()
	if err != nil {
		return nil, nil, err
	}

	al, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		return nil, nil, fmt.Errorf("init proc thread attribute list: %w", err)
	}
	defer al.Delete()

	handles := ioHandles.inheritHandles()
	if err := al.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		return nil, nil, fmt.Errorf("update handle list attribute: %w", err)
	}

	desktopPtr, err := windows.UTF16PtrFromString(desktopName)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		return nil, nil, err
	}
	appName, cmdLine := buildWindowsCommandLine(agyBin, args)
	appPtr, err := windows.UTF16PtrFromString(appName)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		return nil, nil, err
	}
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		return nil, nil, err
	}

	envBlock := createEnvBlock(env)

	var si windows.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Desktop = desktopPtr
	si.StartupInfo.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.StartupInfo.ShowWindow = windows.SW_HIDE
	si.StartupInfo.StdInput = ioHandles.nulHandle
	si.StartupInfo.StdOutput = ioHandles.stdoutWrite
	si.StartupInfo.StdErr = ioHandles.stderrWrite
	si.ProcThreadAttributeList = al.List()

	job := createKillOnCloseJob()
	const createNoWindow = 0x08000000
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED | createNoWindow)
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		appPtr,
		cmdLinePtr,
		nil,
		nil,
		true,
		flags,
		&envBlock[0],
		nil,
		&si.StartupInfo,
		&pi,
	)
	runtime.KeepAlive(al)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(envBlock)
	runtime.KeepAlive(&si)
	runtime.KeepAlive(desktopPtr)
	runtime.KeepAlive(appPtr)
	runtime.KeepAlive(cmdLinePtr)

	ioHandles.closeWriters()

	if err != nil {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		ioHandles.closeReaders()
		return nil, nil, fmt.Errorf("create process: %w", err)
	}

	return runCreatedProcess(ctx, &pi, job, ioHandles, closeDesktopOnce)
}

func execUsageCommandConPTY(ctx context.Context, agyBin string, args []string, env []string) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	var ptyInRead, ptyInWrite windows.Handle
	if err := windows.CreatePipe(&ptyInRead, &ptyInWrite, nil, 0); err != nil {
		return nil, nil, fmt.Errorf("create conpty input pipe: %w", err)
	}
	var ptyOutRead, ptyOutWrite windows.Handle
	if err := windows.CreatePipe(&ptyOutRead, &ptyOutWrite, nil, 0); err != nil {
		_ = windows.CloseHandle(ptyInRead)
		_ = windows.CloseHandle(ptyInWrite)
		return nil, nil, fmt.Errorf("create conpty output pipe: %w", err)
	}

	var hPC windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 25}, ptyInRead, ptyOutWrite, 0, &hPC); err != nil {
		_ = windows.CloseHandle(ptyInRead)
		_ = windows.CloseHandle(ptyInWrite)
		_ = windows.CloseHandle(ptyOutRead)
		_ = windows.CloseHandle(ptyOutWrite)
		return nil, nil, fmt.Errorf("%w: %v", errConPTYUnavailable, err)
	}

	_ = windows.CloseHandle(ptyInRead)
	_ = windows.CloseHandle(ptyOutWrite)

	ptyOutFile := os.NewFile(uintptr(ptyOutRead), "conpty-out")
	ptyOutDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, ptyOutFile)
		_ = ptyOutFile.Close()
		close(ptyOutDone)
	}()

	conptyClosed := false
	closeConPTY := func() {
		if !conptyClosed {
			conptyClosed = true
			windows.ClosePseudoConsole(hPC)
			_ = windows.CloseHandle(ptyInWrite)
			<-ptyOutDone
		}
	}

	ioHandles, err := newChildStdIO()
	if err != nil {
		closeConPTY()
		return nil, nil, err
	}

	al, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, fmt.Errorf("init proc thread attribute list: %w", err)
	}
	defer al.Delete()

	r1, _, callErr := procUpdateProcThreadAttrPtr.Call(
		uintptr(unsafe.Pointer(al.List())),
		0,
		uintptr(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE),
		uintptr(hPC),
		unsafe.Sizeof(hPC),
		0,
		0,
	)
	if r1 == 0 {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, fmt.Errorf("update pseudoconsole attribute: %w", callErr)
	}

	handles := ioHandles.inheritHandles()
	if err := al.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&handles[0]),
		uintptr(len(handles))*unsafe.Sizeof(handles[0]),
	); err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, fmt.Errorf("update handle list attribute: %w", err)
	}

	appName, cmdLine := buildWindowsCommandLine(agyBin, args)
	appPtr, err := windows.UTF16PtrFromString(appName)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, err
	}
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		ioHandles.closeWriters()
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, err
	}

	envBlock := createEnvBlock(env)

	var si windows.StartupInfoEx
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.StartupInfo.ShowWindow = windows.SW_HIDE
	si.StartupInfo.StdInput = ioHandles.nulHandle
	si.StartupInfo.StdOutput = ioHandles.stdoutWrite
	si.StartupInfo.StdErr = ioHandles.stderrWrite
	si.ProcThreadAttributeList = al.List()

	job := createKillOnCloseJob()
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		appPtr,
		cmdLinePtr,
		nil,
		nil,
		true,
		flags,
		&envBlock[0],
		nil,
		&si.StartupInfo,
		&pi,
	)
	runtime.KeepAlive(al)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(envBlock)
	runtime.KeepAlive(&si)
	runtime.KeepAlive(appPtr)
	runtime.KeepAlive(cmdLinePtr)

	ioHandles.closeWriters()

	if err != nil {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
		ioHandles.closeReaders()
		closeConPTY()
		return nil, nil, fmt.Errorf("create process: %w", err)
	}

	return runCreatedProcess(ctx, &pi, job, ioHandles, closeConPTY)
}
