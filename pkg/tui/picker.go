package tui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	tea "github.com/charmbracelet/bubbletea"
)

type quotaFetchedMsg struct {
	index int
	info  *quota.QuotaInfo
	err   error
}

type netCheckMsg struct {
	state string
	cc    string
}

type PickerOptions struct {
	ActiveName string
	Action     string // "switch" (default) or "logout"
	NetCheckFn func(context.Context) (string, string)
}

type PickerModel struct {
	profiles   []profile.Profile
	quotas     map[int]*quota.QuotaInfo
	stale      map[string]bool
	widths     AccountBlockWidths
	cursor     int
	selected   string
	activeName string
	action     string
	quitting   bool
	refreshing int
	lastErr    string
	netWarn    string
	netCheckFn func(context.Context) (string, string)
	qc         *quota.Client
	ctx        context.Context
	pm         *profile.Manager
}

func NewPickerModel(profiles []profile.Profile, qc *quota.Client) PickerModel {
	return NewPickerModelWithOptions(profiles, qc, PickerOptions{})
}

func NewPickerModelWithOptions(profiles []profile.Profile, qc *quota.Client, opts PickerOptions) PickerModel {
	action := opts.Action
	if action == "" {
		action = "switch"
	}
	quotas := make(map[int]*quota.QuotaInfo, len(profiles))
	if qc != nil && len(profiles) > 0 {
		emails := make([]string, len(profiles))
		for i, p := range profiles {
			emails[i] = p.Email
		}
		infos, found := qc.CachedBatch(emails)
		for i := range profiles {
			if i < len(found) && found[i] {
				quotas[i] = &infos[i]
				if infos[i].Tier != "?" && infos[i].Tier != "" {
					profiles[i].Tier = infos[i].Tier
				}
			}
		}
	}

	cursor := 0
	if opts.ActiveName != "" {
		for i, p := range profiles {
			if p.Name == opts.ActiveName {
				cursor = i
				break
			}
		}
	}

	return PickerModel{
		profiles:   profiles,
		quotas:     quotas,
		stale:      make(map[string]bool),
		widths:     ComputeAccountBlockWidths(profiles),
		cursor:     cursor,
		activeName: opts.ActiveName,
		action:     action,
		netCheckFn: opts.NetCheckFn,
		qc:         qc,
		ctx:        context.Background(),
	}
}

func (m PickerModel) Cursor() int {
	return m.cursor
}

func (m PickerModel) Selected() string {
	return m.selected
}

func (m PickerModel) Profiles() []profile.Profile {
	return m.profiles
}

func (m PickerModel) fetchQuotaCmd(idx int, force bool) tea.Cmd {
	if m.qc == nil || idx >= len(m.profiles) {
		return nil
	}
	p := m.profiles[idx]
	qc := m.qc
	return func() tea.Msg {
		info, err := qc.FetchContext(m.ctx, &p.Token, force)
		if info != nil || err != nil {
			return quotaFetchedMsg{index: idx, info: info, err: err}
		}
		return nil
	}
}

func (m PickerModel) netCheckCmd() tea.Cmd {
	fn := m.netCheckFn
	if fn == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		state, cc := fn(ctx)
		return netCheckMsg{state: state, cc: cc}
	}
}

func (m PickerModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.netCheckFn != nil {
		cmds = append(cmds, m.netCheckCmd())
	}
	if m.action != "logout" && m.qc != nil && len(m.profiles) > 0 {
		for i := range m.profiles {
			cmds = append(cmds, m.fetchQuotaCmd(i, false))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func formatQuotaErr(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	const maxLen = 120
	if len(msg) > maxLen {
		return msg[:maxLen-3] + "..."
	}
	return msg
}

func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case netCheckMsg:
		switch msg.state {
		case "blocked":
			m.netWarn = "cannot reach Google from this network (configure proxy: agy-switch proxy)"
		default:
			m.netWarn = ""
		}
		return m, nil

	case quotaFetchedMsg:
		if m.refreshing > 0 {
			m.refreshing--
		}
		if msg.index < len(m.profiles) {
			name := m.profiles[msg.index].Name
			widthsDirty := false
			if msg.info != nil {
				m.quotas[msg.index] = msg.info
				if msg.info.Tier != "?" && msg.info.Tier != "" && msg.info.Tier != m.profiles[msg.index].Tier {
					m.profiles[msg.index].Tier = msg.info.Tier
					widthsDirty = true
				}
				if msg.info.Email != "" && msg.info.Email != m.profiles[msg.index].Email {
					m.profiles[msg.index].Email = msg.info.Email
					m.profiles[msg.index].Token.Email = msg.info.Email
					widthsDirty = true
				}
				if msg.info.RefreshedToken != nil {
					m.profiles[msg.index].Token = *msg.info.RefreshedToken
					if m.pm != nil {
						if updated, err := profile.UpdateRawToken(msg.info.RefreshedToken); err == nil {
							_ = m.pm.UpdateToken(name, updated)
						}
					}
				}
			}
			if widthsDirty {
				m.widths = ComputeAccountBlockWidths(m.profiles)
			}
			if msg.err != nil {
				m.stale[name] = true
				m.lastErr = formatQuotaErr(msg.err)
			} else {
				delete(m.stale, name)
			}
		} else if msg.err != nil {
			m.lastErr = formatQuotaErr(msg.err)
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
		case "r", "R":
			if m.action != "logout" && m.qc != nil {
				m.refreshing = len(m.profiles)
				m.lastErr = ""
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
		case "q", "Q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m PickerModel) View() string {
	if m.quitting {
		return ""
	}

	var sb strings.Builder
	sb.Grow(len(m.profiles)*320 + 160)
	subtitle := "Account Picker"
	if m.action == "logout" {
		subtitle = "Logout Account"
	}
	sb.WriteString(TitleStyle.Render(formatTitle(subtitle)) + "\n\n")

	if len(m.profiles) == 0 {
		sb.WriteString(DimStyle.Render("No profiles found. Run 'agy-switch login' to add one.\n"))
	} else {
		now := time.Now()
		for i, p := range m.profiles {
			isActive := m.activeName != "" && p.Name == m.activeName
			WriteAccountBlock(&sb, p, m.quotas[i], m.widths, isActive, i == m.cursor, now)
			sb.WriteByte('\n')
		}
	}

	if m.netWarn != "" {
		sb.WriteString(BadgePro.Render("warning: "+m.netWarn) + "\n")
	}

	if m.refreshing > 0 {
		sb.WriteString(DimStyle.Render("Refreshing quotas...") + "\n")
	} else if len(m.stale) > 0 {
		names := make([]string, 0, len(m.stale))
		for n := range m.stale {
			names = append(names, n)
		}
		sort.Strings(names)
		sb.WriteString(BadgeUltra.Render("Quota refresh failed ("+strings.Join(names, ", ")+"): "+m.lastErr) + "\n")
	}

	if m.action == "logout" {
		sb.WriteString(HelpStyle.Render("↑/↓ navigate • Enter select • q quit\n"))
	} else {
		sb.WriteString(HelpStyle.Render("↑/↓ navigate • Enter switch & launch agy • r refresh quotas • q quit\n"))
	}
	return sb.String()
}

func RunPicker(pm *profile.Manager, qc *quota.Client) (string, error) {
	return RunPickerContext(context.Background(), pm, qc)
}

// RunPickerContext cancels pending requests when the picker closes.
func RunPickerContext(ctx context.Context, pm *profile.Manager, qc *quota.Client) (string, error) {
	return RunPickerWithOptions(ctx, pm, qc, PickerOptions{ActiveName: pm.ActiveName()})
}

func RunPickerWithOptions(ctx context.Context, pm *profile.Manager, qc *quota.Client, opts PickerOptions) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	profiles, err := pm.List()
	if err != nil {
		return "", err
	}
	if opts.ActiveName == "" {
		opts.ActiveName = pm.ActiveName()
	}
	m := NewPickerModelWithOptions(profiles, qc, opts)
	m.ctx = ctx
	m.pm = pm
	p := tea.NewProgram(m, tea.WithContext(ctx))
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}
	return finalModel.(PickerModel).Selected(), nil
}
