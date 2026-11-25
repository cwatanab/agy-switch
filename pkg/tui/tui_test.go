package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func TestPickerModel_NavigationAndSelect(t *testing.T) {
	profiles := []profile.Profile{
		{Name: "account1", Email: "one@example.com"},
		{Name: "account2", Email: "two@example.com"},
	}

	m := tui.NewPickerModel(profiles, nil)
	if m.Cursor() != 0 {
		t.Fatalf("expected initial cursor 0, got %d", m.Cursor())
	}

	// Down arrow
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tui.PickerModel)
	if m.Cursor() != 1 {
		t.Fatalf("expected cursor 1 after down, got %d", m.Cursor())
	}

	// Enter
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.PickerModel)
	if m.Selected() != "account2" {
		t.Fatalf("expected selected account2, got %s", m.Selected())
	}
	if cmd == nil {
		t.Errorf("expected tea.Quit command on enter, got nil")
	}
}

func TestProxyModel_ToggleActive(t *testing.T) {
	proxies := []proxy.Config{
		{Name: "proxy1", Server: "1.1.1.1", Port: 80, Active: false},
	}

	m := tui.NewProxyModel(proxies, nil)

	// Space key toggles
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(tui.ProxyModel)

	list := m.Proxies()
	if len(list) == 0 || !list[0].Active {
		t.Fatalf("expected proxy1 to be active after Space toggle, got: %+v", list)
	}
}

func sendKeys(m tui.ProxyModel, text string) (tui.ProxyModel, tea.Cmd) {
	var cmd tea.Cmd
	for _, r := range text {
		var updated tea.Model
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(tui.ProxyModel)
	}
	return m, cmd
}

func TestProxyModel_EnterEnableRenameDeleteEditAndAdd(t *testing.T) {
	paths := &config.Paths{DataDir: t.TempDir()}
	store := proxy.NewStore(paths)
	if err := store.Save(proxy.Config{
		Name: "p1", Host: "1.2.3.4", Server: "1.2.3.4", Port: 1080, Protocol: "socks5h",
		Check: &proxy.ProbeResult{At: time.Now().Unix(), OK: true, ExitIP: "9.9.9.9", CC: "JP", City: "Tokyo", Kind: "residential/ISP"},
	}); err != nil {
		t.Fatal(err)
	}
	list, _ := store.List()
	m := tui.NewProxyModelWithOptions(list, store, tui.ProxyOptions{
		DisableAutoProbe: true,
		RaceFn:           func(string, int, string, string) (string, error) { return "https", nil },
		EnvProxyFn:       func() *proxy.Config { return nil },
		ProbeFn: func(context.Context, proxy.Config) *proxy.ProbeResult {
			return &proxy.ProbeResult{At: time.Now().Unix(), OK: true, CC: "US", Kind: "hosting"}
		},
	})

	if view := m.View(); !strings.Contains(view, "Tokyo") || !strings.Contains(view, "residential/ISP") {
		t.Fatalf("expected probe details in proxy list view, got:\n%s", view)
	}

	// Rename via 'R'
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "work")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	if m.Proxies()[0].Name != "work" {
		t.Fatalf("expected renamed proxy 'work', got %+v", m.Proxies())
	}

	// Edit port via 'e'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(tui.ProxyModel)
	for i := 0; i < 3; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(tui.ProxyModel)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "2080")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(tui.ProxyModel)
	if m.Proxies()[0].Port != 2080 {
		t.Fatalf("expected updated port 2080, got %d", m.Proxies()[0].Port)
	}

	// Add wizard via 'a'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "proxy.example.com") // server
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "8443") // port
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // username empty
	m = updated.(tui.ProxyModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // password empty
	m = updated.(tui.ProxyModel)
	// protocol empty -> auto race
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	if cmd == nil {
		t.Fatal("expected async race cmd")
	}
	updated, _ = m.Update(cmd())
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "30d") // validity
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	m, _ = sendKeys(m, "y") // fallback
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm save & enable
	m = updated.(tui.ProxyModel)
	if len(m.Proxies()) != 2 {
		t.Fatalf("expected 2 proxies after add, got %d", len(m.Proxies()))
	}

	// Delete via 'd' + 'y'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(tui.ProxyModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tui.ProxyModel)
	if len(m.Proxies()) != 1 {
		t.Fatalf("expected 1 proxy after delete, got %d", len(m.Proxies()))
	}

	// Enter activates remaining proxy and quits
	updated, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tui.ProxyModel)
	if !m.Proxies()[0].Active || quitCmd == nil || !strings.Contains(m.ExitMessage(), "proxy on") {
		t.Fatalf("expected active proxy and quit on Enter, got active=%v exit=%q", m.Proxies()[0].Active, m.ExitMessage())
	}
}
