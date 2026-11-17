package tui

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	tea "github.com/charmbracelet/bubbletea"
)

type quotaFetchedMsg struct {
	index int
	info  *quota.QuotaInfo
}

type PickerModel struct {
	profiles []profile.Profile
	quotas   map[int]*quota.QuotaInfo
	cursor   int
	selected string
	quitting bool
	qc       *quota.Client
	ctx      context.Context
}

func NewPickerModel(profiles []profile.Profile, qc *quota.Client) PickerModel {
	quotas := make(map[int]*quota.QuotaInfo)
	if qc != nil {
		cached := qc.CachedAll()
		for i, p := range profiles {
			if qInfo, ok := cached[p.Email]; ok {
				quotas[i] = &qInfo
				if qInfo.Tier != "?" && qInfo.Tier != "" {
					profiles[i].Tier = qInfo.Tier
				}
			}
		}
	}

	return PickerModel{
		profiles: profiles,
		quotas:   quotas,
		cursor:   0,
		qc:       qc,
		ctx:      context.Background(),
	}
}

func (m PickerModel) Cursor() int {
	return m.cursor
}

func (m PickerModel) Selected() string {
	return m.selected
}

func (m PickerModel) fetchQuotaCmd(idx int, force bool) tea.Cmd {
	if m.qc == nil || idx >= len(m.profiles) {
		return nil
	}
	p := m.profiles[idx]
	qc := m.qc
	return func() tea.Msg {
		info, _ := qc.FetchContext(m.ctx, &p.Token, force)
		if info != nil {
			return quotaFetchedMsg{index: idx, info: info}
		}
		return nil
	}
}

func (m PickerModel) Init() tea.Cmd {
	if m.qc == nil || len(m.profiles) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for i := range m.profiles {
		cmds = append(cmds, m.fetchQuotaCmd(i, false))
	}
	return tea.Batch(cmds...)
}

func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case quotaFetchedMsg:
		if msg.index < len(m.profiles) {
			m.quotas[msg.index] = msg.info
			if msg.info.Tier != "?" && msg.info.Tier != "" {
				m.profiles[msg.index].Tier = msg.info.Tier
			}
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			} else if len(m.profiles) > 0 {
				m.cursor = len(m.profiles) - 1
			}
		case "down", "j":
			if m.cursor < len(m.profiles)-1 {
				m.cursor++
			} else {
				m.cursor = 0
			}
		case "r":
			// Refresh all quotas in background
			if m.qc != nil {
				var cmds []tea.Cmd
				for i := range m.profiles {
					cmds = append(cmds, m.fetchQuotaCmd(i, true))
				}
				return m, tea.Batch(cmds...)
			}
		case "enter":
			if len(m.profiles) > 0 && m.cursor < len(m.profiles) {
				m.selected = m.profiles[m.cursor].Name
				m.quitting = true
				return m, tea.Quit
			}
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func renderBar(fraction float64, width int, hasQuota bool) string {
	if !hasQuota {
		return DimStyle.Render("["+strings.Repeat("─", width)+"]") + "    —"
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1.0 {
		fraction = 1.0
	}
	filled := int(fraction * float64(width))
	if filled > width {
		filled = width
	}
	pct := int(math.Round(fraction * 100))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	style := CursorStyle
	if pct <= 20 {
		style = BadgeUltra
	} else if pct <= 50 {
		style = BadgePro
	}
	return style.Render("["+bar+"]") + fmt.Sprintf(" %3d%%", pct)
}

func (m PickerModel) View() string {
	if m.quitting {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(TitleStyle.Render("✦ agy-switch — Account Picker") + "\n\n")

	if len(m.profiles) == 0 {
		sb.WriteString(DimStyle.Render("No profiles found. Run 'agy-switch login' to add one.\n"))
	} else {
		for i, p := range m.profiles {
			isCursor := i == m.cursor
			cursorStr := "  "
			if isCursor {
				cursorStr = CursorStyle.Render("▸ ")
			}

			tierBadge := DimStyle.Render("[?]")
			if p.Tier == "Pro" {
				tierBadge = BadgePro.Render("[Pro]")
			} else if p.Tier == "Ultra" {
				tierBadge = BadgeUltra.Render("[Ultra]")
			} else if p.Tier == "Free" {
				tierBadge = BadgeFree.Render("[Free]")
			}

			quotaStr := ""
			if q, ok := m.quotas[i]; ok && q != nil {
				gemini := renderBar(q.Gemini5hFraction, 8, q.HasGemini)
				claude := renderBar(q.Claude5hFraction, 8, q.HasClaude)
				quotaStr = fmt.Sprintf("  Gemini: %s  Claude: %s", gemini, claude)
			}

			row := fmt.Sprintf("%-14s %s  %-24s%s", p.Name, tierBadge, DimStyle.Render(p.Email), quotaStr)
			if isCursor {
				sb.WriteString(cursorStr + SelectedStyle.Render(row) + "\n")
			} else {
				sb.WriteString(cursorStr + NormalStyle.Render(row) + "\n")
			}
		}
	}

	sb.WriteString("\n" + HelpStyle.Render("↑/↓ navigate • Enter switch & launch agy • r refresh quotas • q quit\n"))
	return sb.String()
}

func RunPicker(pm *profile.Manager, qc *quota.Client) (string, error) {
	return RunPickerContext(context.Background(), pm, qc)
}

// RunPickerContext cancels pending requests when the picker closes.
func RunPickerContext(ctx context.Context, pm *profile.Manager, qc *quota.Client) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	profiles, err := pm.List()
	if err != nil {
		return "", err
	}
	m := NewPickerModel(profiles, qc)
	m.ctx = ctx
	p := tea.NewProgram(m, tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}
	return finalModel.(PickerModel).Selected(), nil
}
