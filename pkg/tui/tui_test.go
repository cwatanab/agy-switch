package tui_test

import (
	"testing"

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
