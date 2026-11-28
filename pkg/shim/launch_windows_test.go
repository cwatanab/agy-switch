//go:build windows

package shim

import (
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestBuildLaunchCommandLine(t *testing.T) {
	app, cmdLine := buildLaunchCommandLine(`C:\Program Files\Antigravity\agy.exe`, []string{"--model", "gemini"})
	if app != `C:\Program Files\Antigravity\agy.exe` {
		t.Fatalf("app = %q", app)
	}
	want := `"C:\Program Files\Antigravity\agy.exe" --model gemini`
	if cmdLine != want {
		t.Fatalf("cmdLine = %q, want %q", cmdLine, want)
	}

	appNoArgs, cmdNoArgs := buildLaunchCommandLine(`C:\bin\agy.exe`, nil)
	if appNoArgs != `C:\bin\agy.exe` || cmdNoArgs != `C:\bin\agy.exe` {
		t.Fatalf("got (%q, %q)", appNoArgs, cmdNoArgs)
	}

	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	for _, bin := range []string{`C:\Users\alice smith\bin\agy.cmd`, `C:\bin\agy.BAT`} {
		appCmd, cmdLineCmd := buildLaunchCommandLine(bin, nil)
		if appCmd != comspec {
			t.Fatalf("appCmd = %q, want %q", appCmd, comspec)
		}
		if !strings.Contains(cmdLineCmd, `/d /c "`) || !strings.Contains(cmdLineCmd, bin) {
			t.Fatalf("unexpected cmdLineCmd = %q", cmdLineCmd)
		}
	}
}

func TestCreateWindowsEnvBlock(t *testing.T) {
	empty := createWindowsEnvBlock(nil)
	if len(empty) != 2 || empty[0] != 0 || empty[1] != 0 {
		t.Fatalf("empty env block = %v, want [0 0]", empty)
	}

	block := createWindowsEnvBlock([]string{"zeta=1", "", "bad\x00=x", "alpha=two", "Beta=3"})
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatalf("missing double NUL terminator: %v", block)
	}

	decoded := string(utf16.Decode(block[:len(block)-2]))
	got := strings.Split(decoded, "\x00")
	want := []string{"alpha=two", "Beta=3", "zeta=1"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q (all=%q)", i, got[i], want[i], got)
		}
	}
}
