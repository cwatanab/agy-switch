package tui

import (
	"fmt"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	tea "github.com/charmbracelet/bubbletea"
)

type ProxyModel struct {
	proxies  []proxy.Config
	cursor   int
	store    *proxy.Store
	quitting bool
}

func NewProxyModel(proxies []proxy.Config, store *proxy.Store) ProxyModel {
	return ProxyModel{
		proxies: proxies,
		cursor:  0,
		store:   store,
	}
}

func (m ProxyModel) Proxies() []proxy.Config {
	return m.proxies
}

func (m ProxyModel) Cursor() int {
	return m.cursor
}

func (m ProxyModel) Init() tea.Cmd {
	return nil
}

func (m ProxyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			} else if len(m.proxies) > 0 {
				m.cursor = len(m.proxies) - 1
			}
		case "down", "j":
			if m.cursor < len(m.proxies)-1 {
				m.cursor++
			} else {
				m.cursor = 0
			}
		case " ":
			if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
				curr := &m.proxies[m.cursor]
				curr.Active = !curr.Active
				// If activated, deactivate others
				if curr.Active {
					for i := range m.proxies {
						if i != m.cursor {
							m.proxies[i].Active = false
						}
					}
				}
				if m.store != nil {
					_ = m.store.SetActive(curr.Name, curr.Active)
				}
			}
		case "enter":
			m.quitting = true
			return m, tea.Quit
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m ProxyModel) View() string {
	if m.quitting {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(TitleStyle.Render("✦ agy-switch — Proxy Manager") + "\n\n")

	if len(m.proxies) == 0 {
		sb.WriteString(DimStyle.Render("No proxies configured. Run 'agy-switch proxy add <name> --server <host> --port <port>'.\n"))
	} else {
		for i, p := range m.proxies {
			isCursor := i == m.cursor
			cursorStr := "  "
			if isCursor {
				cursorStr = CursorStyle.Render("▸ ")
			}

			activeStr := DimStyle.Render("[ ]")
			if p.Active {
				activeStr = ActiveMarker.Render("[*]")
			}

			server := p.GetServer()
			row := fmt.Sprintf("%s %-16s %-8s %s:%d", activeStr, p.Name, p.Protocol, server, p.Port)
			if isCursor {
				sb.WriteString(cursorStr + SelectedStyle.Render(row) + "\n")
			} else {
				sb.WriteString(cursorStr + NormalStyle.Render(row) + "\n")
			}
		}
	}

	sb.WriteString("\n" + HelpStyle.Render("↑/↓ navigate • space toggle active • Enter done • q quit\n"))
	return sb.String()
}

func RunProxyManager(ps *proxy.Store) error {
	list, err := ps.List()
	if err != nil {
		return err
	}
	m := NewProxyModel(list, ps)
	p := tea.NewProgram(m)
	_, err = p.Run()
	return err
}
