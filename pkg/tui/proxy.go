package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/util"
	tea "github.com/charmbracelet/bubbletea"
)

type proxyMode int

const (
	proxyModeList proxyMode = iota
	proxyModeConfirmDelete
	proxyModeRename
	proxyModeEdit
	proxyModeAdd
)

type proxyProbedMsg struct {
	name string
	res  *proxy.ProbeResult
}

type proxyRaceMsg struct {
	proto string
	err   error
}

type ProxyOptions struct {
	ProbeFn          func(context.Context, proxy.Config) *proxy.ProbeResult
	RaceFn           func(server string, port int, user, pass string) (string, error)
	EnvProxyFn       func() *proxy.Config
	OnEnable         func(name string) error
	OnDisable        func() error
	DisableAutoProbe bool
}

type editField struct {
	name  string
	value string
}

type addWizardState struct {
	step      int // 0..6 inputs, 7 dupe confirm, 8 final confirm
	cfg       proxy.Config
	validity  string
	fallback  string
	input     string
	racing    bool
	errMsg    string
	dupeName  string
	detected  bool
	envNote   bool
}

type editState struct {
	origName   string
	fields     []editField
	cursor     int
	editing    bool
	input      string
	dirty      bool
	errMsg     string
	expiresAt  int64
}

type ProxyModel struct {
	proxies  []proxy.Config
	checks   map[string]*proxy.ProbeResult
	probing  map[string]bool
	cursor   int
	store    *proxy.Store
	opts     ProxyOptions
	mode     proxyMode
	status   string
	confirm  string
	rename   string
	edit     editState
	add      addWizardState
	quitting bool
	exitMsg  string
	ctx      context.Context
}

func NewProxyModel(proxies []proxy.Config, store *proxy.Store) ProxyModel {
	return NewProxyModelWithOptions(proxies, store, ProxyOptions{
		DisableAutoProbe: store == nil,
	})
}

func NewProxyModelWithOptions(proxies []proxy.Config, store *proxy.Store, opts ProxyOptions) ProxyModel {
	if opts.ProbeFn == nil {
		opts.ProbeFn = proxy.ProbeProxy
	}
	if opts.RaceFn == nil {
		opts.RaceFn = proxy.RaceProtocol
	}
	if opts.EnvProxyFn == nil {
		opts.EnvProxyFn = proxy.FromEnv
	}
	checks := make(map[string]*proxy.ProbeResult, len(proxies))
	for _, p := range proxies {
		if p.Check != nil {
			cp := *p.Check
			checks[p.Name] = &cp
		}
	}
	return ProxyModel{
		proxies: proxies,
		checks:  checks,
		probing: make(map[string]bool),
		store:   store,
		opts:    opts,
		ctx:     context.Background(),
	}
}

func (m ProxyModel) Proxies() []proxy.Config {
	return m.proxies
}

func (m ProxyModel) Cursor() int {
	return m.cursor
}

func (m ProxyModel) ExitMessage() string {
	return m.exitMsg
}

func (m ProxyModel) Mode() int {
	return int(m.mode)
}

func (m ProxyModel) Status() string {
	return m.status
}

func (m ProxyModel) probeCmd(cfg proxy.Config) tea.Cmd {
	probeFn := m.opts.ProbeFn
	if probeFn == nil {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		res := probeFn(ctx, cfg)
		return proxyProbedMsg{name: cfg.Name, res: res}
	}
}

func (m *ProxyModel) kickProbes(force bool) tea.Cmd {
	if m.opts.ProbeFn == nil || len(m.proxies) == 0 {
		return nil
	}
	now := time.Now()
	var cmds []tea.Cmd
	for _, p := range m.proxies {
		chk := m.checks[p.Name]
		if !force && chk != nil && now.Sub(time.Unix(chk.At, 0)) < proxy.ProbeTTL {
			continue
		}
		m.probing[p.Name] = true
		cmds = append(cmds, m.probeCmd(p))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m ProxyModel) Init() tea.Cmd {
	if m.opts.DisableAutoProbe {
		return nil
	}
	return m.kickProbes(false)
}

func (m *ProxyModel) enableSelected(name string) {
	for i := range m.proxies {
		m.proxies[i].Active = (m.proxies[i].Name == name)
	}
	if m.store != nil {
		if err := m.store.SetActive(name, true); err != nil {
			m.status = BadgeUltra.Render(err.Error())
			return
		}
	}
	if m.opts.OnEnable != nil {
		if err := m.opts.OnEnable(name); err != nil {
			m.status = BadgeUltra.Render(err.Error())
			return
		}
	}
	m.status = ActiveMarker.Render("✓ '" + name + "' ENABLED — every agy run uses it")
}

func (m *ProxyModel) disableActive(name string) {
	for i := range m.proxies {
		if m.proxies[i].Name == name {
			m.proxies[i].Active = false
		}
	}
	if m.store != nil {
		if err := m.store.SetActive(name, false); err != nil {
			m.status = BadgeUltra.Render(err.Error())
			return
		}
	}
	if m.opts.OnDisable != nil {
		if err := m.opts.OnDisable(); err != nil {
			m.status = BadgeUltra.Render(err.Error())
			return
		}
	}
	m.status = "'" + name + "' disabled — agy runs direct"
}

func (m *ProxyModel) reloadFromStore() {
	if m.store == nil {
		return
	}
	if list, err := m.store.List(); err == nil {
		m.proxies = list
		if m.cursor >= len(m.proxies) {
			m.cursor = max(0, len(m.proxies)-1)
		}
	}
}

func appendKeyText(buf string, msg tea.KeyMsg) (string, bool) {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		if len(buf) > 0 {
			_, size := utf8.DecodeLastRuneInString(buf)
			return buf[:len(buf)-size], true
		}
		return buf, true
	case tea.KeySpace:
		return buf + " ", true
	case tea.KeyRunes:
		return buf + string(msg.Runes), true
	}
	return buf, false
}

func (m ProxyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case proxyProbedMsg:
		delete(m.probing, msg.name)
		if msg.res != nil {
			m.checks[msg.name] = msg.res
			if m.store != nil {
				_ = m.store.SaveProbe(msg.name, msg.res)
			}
		}
		if len(m.probing) == 0 && m.status == "" {
			m.status = ActiveMarker.Render("✓ probes updated")
		}
		return m, nil

	case proxyRaceMsg:
		m.add.racing = false
		if msg.err != nil || msg.proto == "" {
			m.add.errMsg = "auto-detect failed — type http, https, socks5, or socks5h"
			m.add.input = ""
			return m, nil
		}
		m.add.cfg.Protocol = msg.proto
		m.add.errMsg = ""
		m.add.step = 5
		m.add.input = ""
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case proxyModeConfirmDelete:
			return m.updateConfirmDelete(msg)
		case proxyModeRename:
			return m.updateRename(msg)
		case proxyModeEdit:
			return m.updateEdit(msg)
		case proxyModeAdd:
			return m.updateAdd(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

func (m ProxyModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if len(m.proxies) > 0 {
			if m.cursor > 0 {
				m.cursor--
			} else {
				m.cursor = len(m.proxies) - 1
			}
		}
	case "down", "j":
		if len(m.proxies) > 0 {
			if m.cursor < len(m.proxies)-1 {
				m.cursor++
			} else {
				m.cursor = 0
			}
		}
	case " ":
		if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
			curr := m.proxies[m.cursor]
			if curr.Active {
				m.disableActive(curr.Name)
			} else {
				m.enableSelected(curr.Name)
			}
		}
	case "enter":
		if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
			curr := m.proxies[m.cursor]
			if !curr.Active {
				m.enableSelected(curr.Name)
			}
			for _, p := range m.proxies {
				if p.Name == curr.Name && p.Active {
					m.exitMsg = fmt.Sprintf("proxy on (%s: %s)", p.Name, p.RawURL(true))
					break
				}
			}
		}
		m.quitting = true
		return m, tea.Quit
	case "a":
		m.startAddWizard()
		return m, nil
	case "e":
		if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
			m.startEdit(m.proxies[m.cursor])
		}
		return m, nil
	case "R":
		if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
			m.mode = proxyModeRename
			m.rename = ""
			m.status = ""
		}
		return m, nil
	case "d":
		if len(m.proxies) > 0 && m.cursor < len(m.proxies) {
			m.confirm = m.proxies[m.cursor].Name
			m.mode = proxyModeConfirmDelete
		}
		return m, nil
	case "r":
		m.status = ""
		return m, m.kickProbes(true)
	case "q", "Q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

func (m ProxyModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		name := m.confirm
		wasActive := false
		for _, p := range m.proxies {
			if p.Name == name && p.Active {
				wasActive = true
				break
			}
		}
		if m.store != nil {
			if err := m.store.Delete(name); err != nil {
				m.status = BadgeUltra.Render(err.Error())
				m.mode = proxyModeList
				m.confirm = ""
				return m, nil
			}
		}
		if wasActive && m.opts.OnDisable != nil {
			_ = m.opts.OnDisable()
		}
		filtered := m.proxies[:0]
		for _, p := range m.proxies {
			if p.Name != name {
				filtered = append(filtered, p)
			}
		}
		m.proxies = filtered
		delete(m.checks, name)
		delete(m.probing, name)
		if m.cursor >= len(m.proxies) {
			m.cursor = max(0, len(m.proxies)-1)
		}
		m.status = fmt.Sprintf("deleted '%s'", name)
	}
	m.confirm = ""
	m.mode = proxyModeList
	return m, nil
}

func (m ProxyModel) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = proxyModeList
		m.rename = ""
		return m, nil
	case tea.KeyEnter:
		oldName := m.proxies[m.cursor].Name
		newName := strings.TrimSpace(m.rename)
		m.mode = proxyModeList
		m.rename = ""
		if newName == "" || newName == oldName {
			return m, nil
		}
		if err := util.ValidateName(newName); err != nil {
			m.status = BadgeUltra.Render("invalid name — letters, digits, . _ - only")
			return m, nil
		}
		for _, p := range m.proxies {
			if p.Name == newName {
				m.status = BadgeUltra.Render("'" + newName + "' already exists")
				return m, nil
			}
		}
		if m.store != nil {
			if err := m.store.Rename(oldName, newName); err != nil {
				m.status = BadgeUltra.Render(err.Error())
				return m, nil
			}
		}
		m.proxies[m.cursor].Name = newName
		if chk, ok := m.checks[oldName]; ok {
			m.checks[newName] = chk
			delete(m.checks, oldName)
		}
		sort.Slice(m.proxies, func(i, j int) bool { return m.proxies[i].Name < m.proxies[j].Name })
		for i, p := range m.proxies {
			if p.Name == newName {
				m.cursor = i
				break
			}
		}
		m.status = fmt.Sprintf("renamed '%s' → '%s'", oldName, newName)
		return m, nil
	}
	if next, ok := appendKeyText(m.rename, msg); ok {
		m.rename = next
	}
	return m, nil
}

func (m *ProxyModel) startEdit(cfg proxy.Config) {
	valStr := ""
	if cfg.ExpiresAt > 0 {
		valStr = time.Unix(cfg.ExpiresAt, 0).Format("2006-01-02")
	}
	fbStr := "no"
	if cfg.Fallback {
		fbStr = "yes"
	}
	m.edit = editState{
		origName:  cfg.Name,
		expiresAt: cfg.ExpiresAt,
		fields: []editField{
			{name: "name", value: cfg.Name},
			{name: "protocol", value: cfg.Protocol},
			{name: "host", value: cfg.GetServer()},
			{name: "port", value: strconv.Itoa(cfg.Port)},
			{name: "username", value: cfg.Username},
			{name: "password", value: cfg.Password},
			{name: "validity", value: valStr},
			{name: "fallback", value: fbStr},
		},
	}
	m.mode = proxyModeEdit
}

func (m ProxyModel) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.edit.editing {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.edit.editing = false
			m.edit.input = ""
			m.edit.errMsg = ""
			return m, nil
		case tea.KeyEnter:
			f := &m.edit.fields[m.edit.cursor]
			val := strings.TrimSpace(m.edit.input)
			if val == "" {
				m.edit.editing = false
				m.edit.input = ""
				return m, nil
			}
			switch f.name {
			case "name":
				if err := util.ValidateName(val); err != nil {
					m.edit.errMsg = "letters, digits, . _ - only"
					return m, nil
				}
				for _, p := range m.proxies {
					if p.Name == val && val != m.edit.origName {
						m.edit.errMsg = fmt.Sprintf("'%s' already exists", val)
						return m, nil
					}
				}
			case "protocol":
				val = strings.ToLower(val)
				if val != "http" && val != "https" && val != "socks5" && val != "socks5h" {
					m.edit.errMsg = "http / https / socks5 / socks5h"
					return m, nil
				}
			case "port":
				port, err := strconv.Atoi(val)
				if err != nil || port < 1 || port > 65535 {
					m.edit.errMsg = "port must be 1-65535"
					return m, nil
				}
			case "validity":
				ts, err := util.ParseValidity(val, time.Now())
				if err != nil {
					m.edit.errMsg = err.Error()
					return m, nil
				}
				m.edit.expiresAt = ts
				if ts == 0 {
					val = ""
				} else {
					val = time.Unix(ts, 0).Format("2006-01-02")
				}
			case "fallback":
				low := strings.ToLower(val)
				if low != "y" && low != "yes" && low != "n" && low != "no" {
					m.edit.errMsg = "y or n"
					return m, nil
				}
				if low == "y" || low == "yes" {
					val = "yes"
				} else {
					val = "no"
				}
			}
			f.value = val
			m.edit.dirty = true
			m.edit.editing = false
			m.edit.input = ""
			m.edit.errMsg = ""
			return m, nil
		}
		if next, ok := appendKeyText(m.edit.input, msg); ok {
			m.edit.input = next
		}
		return m, nil
	}

	switch msg.String() {
	case "up", "k":
		if m.edit.cursor > 0 {
			m.edit.cursor--
		} else {
			m.edit.cursor = len(m.edit.fields) - 1
		}
	case "down", "j":
		if m.edit.cursor < len(m.edit.fields)-1 {
			m.edit.cursor++
		} else {
			m.edit.cursor = 0
		}
	case "enter":
		m.edit.editing = true
		m.edit.input = ""
		m.edit.errMsg = ""
	case "d":
		if !m.edit.dirty {
			m.mode = proxyModeList
			return m, nil
		}
		byName := make(map[string]string, len(m.edit.fields))
		for _, f := range m.edit.fields {
			byName[f.name] = f.value
		}
		port, _ := strconv.Atoi(byName["port"])
		oldCfg := m.proxies[m.cursor]
		newCfg := proxy.Config{
			Name:      byName["name"],
			Host:      byName["host"],
			Server:    byName["host"],
			Port:      port,
			Protocol:  byName["protocol"],
			Username:  byName["username"],
			Password:  byName["password"],
			Active:    oldCfg.Active,
			ExpiresAt: m.edit.expiresAt,
			Fallback:  byName["fallback"] == "yes",
			Check:     oldCfg.Check,
		}
		if m.store != nil {
			if err := m.store.Update(m.edit.origName, newCfg); err != nil {
				m.edit.errMsg = err.Error()
				return m, nil
			}
			m.reloadFromStore()
		} else {
			m.proxies[m.cursor] = newCfg
		}
		delete(m.checks, m.edit.origName)
		m.mode = proxyModeList
		m.status = fmt.Sprintf("saved '%s'", newCfg.Name)
		if !m.opts.DisableAutoProbe {
			m.probing[newCfg.Name] = true
			return m, m.probeCmd(newCfg)
		}
		return m, nil
	case "q", "Q", "esc", "ctrl+c":
		m.mode = proxyModeList
		return m, nil
	}
	return m, nil
}

func (m *ProxyModel) startAddWizard() {
	var cfg proxy.Config
	detected := false
	if m.opts.EnvProxyFn != nil {
		if envCfg := m.opts.EnvProxyFn(); envCfg != nil {
			cfg = *envCfg
			detected = true
		}
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
		cfg.Server = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 7890
	}
	m.add = addWizardState{
		step:     0,
		cfg:      cfg,
		input:    "",
		detected: detected,
		envNote:  detected,
	}
	m.mode = proxyModeAdd
}

func (m ProxyModel) updateAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEsc || msg.Type == tea.KeyCtrlC {
		m.mode = proxyModeList
		m.status = "cancelled — nothing changed"
		return m, nil
	}
	if m.add.racing {
		return m, nil
	}
	if msg.Type == tea.KeyEnter {
		raw := strings.TrimSpace(m.add.input)
		m.add.errMsg = ""
		switch m.add.step {
		case 0: // server
			if raw != "" {
				m.add.cfg.Host = raw
				m.add.cfg.Server = raw
			}
			if strings.TrimSpace(m.add.cfg.Host) == "" {
				m.add.errMsg = "server is required"
				return m, nil
			}
			m.add.cfg.Name = m.add.cfg.Host
			m.add.step = 1
			m.add.input = ""
		case 1: // port
			pStr := raw
			if pStr == "" && m.add.cfg.Port > 0 {
				pStr = strconv.Itoa(m.add.cfg.Port)
			}
			port, err := strconv.Atoi(pStr)
			if err != nil || port < 1 || port > 65535 {
				m.add.errMsg = "port must be a number between 1 and 65535"
				return m, nil
			}
			m.add.cfg.Port = port
			m.add.step = 2
			m.add.input = ""
		case 2: // username
			if raw != "" {
				m.add.cfg.Username = raw
			}
			m.add.step = 3
			m.add.input = ""
		case 3: // password
			if raw != "" {
				m.add.cfg.Password = raw
			}
			m.add.step = 4
			m.add.input = ""
		case 4: // protocol
			proto := strings.ToLower(raw)
			if proto == "" {
				proto = strings.ToLower(m.add.cfg.Protocol)
			}
			if proto == "" || proto == "auto" {
				if m.opts.RaceFn == nil {
					m.add.errMsg = "enter protocol: http, https, socks5, or socks5h"
					return m, nil
				}
				m.add.racing = true
				cfg := m.add.cfg
				raceFn := m.opts.RaceFn
				return m, func() tea.Msg {
					p, err := raceFn(cfg.GetServer(), cfg.Port, cfg.Username, cfg.Password)
					return proxyRaceMsg{proto: p, err: err}
				}
			}
			if proto != "http" && proto != "https" && proto != "socks5" && proto != "socks5h" {
				m.add.errMsg = "unknown protocol — http, https, socks5, socks5h or empty"
				return m, nil
			}
			m.add.cfg.Protocol = proto
			m.add.step = 5
			m.add.input = ""
		case 5: // validity
			ts, err := util.ParseValidity(raw, time.Now())
			if err != nil {
				m.add.errMsg = err.Error()
				return m, nil
			}
			m.add.cfg.ExpiresAt = ts
			m.add.validity = raw
			m.add.step = 6
			m.add.input = ""
		case 6: // fallback
			low := strings.ToLower(raw)
			m.add.cfg.Fallback = low == "y" || low == "yes"
			m.add.fallback = "no"
			if m.add.cfg.Fallback {
				m.add.fallback = "yes"
			}
			// Check duplicate host:port under different name
			m.add.dupeName = ""
			for _, p := range m.proxies {
				if p.Name != m.add.cfg.Name && p.GetServer() == m.add.cfg.GetServer() && p.Port == m.add.cfg.Port {
					m.add.dupeName = p.Name
					break
				}
			}
			if m.add.dupeName != "" {
				m.add.step = 7
			} else {
				m.add.step = 8
			}
			m.add.input = ""
		case 7: // duplicate prompt [Y/n]
			low := strings.ToLower(raw)
			if low != "n" && low != "no" {
				m.add.cfg.Name = m.add.dupeName
			}
			m.add.step = 8
			m.add.input = ""
		case 8: // save & enable [Y/n]
			low := strings.ToLower(raw)
			if low == "n" || low == "no" {
				m.mode = proxyModeList
				m.status = "cancelled — nothing changed"
				return m, nil
			}
			m.add.cfg.Active = true
			if err := util.ValidateName(m.add.cfg.Name); err != nil {
				m.add.cfg.Name = "proxy"
			}
			if m.store != nil {
				if err := m.store.Save(m.add.cfg); err != nil {
					m.add.errMsg = err.Error()
					return m, nil
				}
				m.reloadFromStore()
			} else {
				replaced := false
				for i := range m.proxies {
					m.proxies[i].Active = false
					if m.proxies[i].Name == m.add.cfg.Name {
						m.proxies[i] = m.add.cfg
						replaced = true
					}
				}
				if !replaced {
					m.proxies = append(m.proxies, m.add.cfg)
				}
			}
			if m.opts.OnEnable != nil {
				_ = m.opts.OnEnable(m.add.cfg.Name)
			}
			savedCfg := m.add.cfg
			m.mode = proxyModeList
			m.status = ActiveMarker.Render("✓ proxy '" + savedCfg.Name + "' saved and ENABLED")
			if !m.opts.DisableAutoProbe {
				m.probing[savedCfg.Name] = true
				return m, m.probeCmd(savedCfg)
			}
			return m, nil
		}
		return m, nil
	}

	if next, ok := appendKeyText(m.add.input, msg); ok {
		m.add.input = next
	}
	return m, nil
}

// FormatProbeLine formats a human-readable probe status line for a proxy.
func FormatProbeLine(chk *proxy.ProbeResult, probing bool, now time.Time) string {
	if probing || chk == nil {
		return DimStyle.Render("probing…")
	}
	if !chk.OK {
		if chk.Err != "" {
			return BadgeUltra.Render("✗ unreachable") + " " + DimStyle.Render("— "+chk.Err)
		}
		return BadgeUltra.Render("✗ unreachable")
	}
	age := util.FormatRelativeTime(time.Unix(chk.At, 0).UTC(), now)
	if chk.ExitIP == "" {
		return ActiveMarker.Render("✓") + " " + DimStyle.Render(age) + "  tunnel ok " + DimStyle.Render("· exit geo unknown")
	}
	loc := strings.TrimSpace(strings.Join([]string{chk.CC, chk.City}, " "))
	kind := chk.Kind
	if kind == "" {
		kind = "?"
	}
	line := ActiveMarker.Render("✓") + " " + DimStyle.Render(age) + "  " + loc + " · " + NormalStyle.Render(kind)
	if chk.ISP != "" {
		line += " · " + chk.ISP
	}
	if proxy.IsBlockedCountry(chk.CC) {
		line += " " + BadgeUltra.Render("· region unsupported by Google AI")
	}
	return line
}

func (m ProxyModel) View() string {
	if m.quitting {
		return ""
	}
	switch m.mode {
	case proxyModeEdit:
		return m.viewEdit()
	case proxyModeAdd:
		return m.viewAdd()
	default:
		return m.viewList()
	}
}

func (m ProxyModel) viewList() string {
	var sb strings.Builder
	sb.WriteString(TitleStyle.Render(formatTitle("Proxy Manager")) + "\n\n")
	now := time.Now()

	if len(m.proxies) == 0 {
		sb.WriteString(DimStyle.Render("  no proxies yet — press 'a' to add one\n"))
	} else {
		for i, p := range m.proxies {
			isCursor := i == m.cursor
			cursorStr := "  "
			if isCursor {
				cursorStr = CursorStyle.Render("▸ ")
			}

			dot := DimStyle.Render("○")
			tag := DimStyle.Render("off")
			if p.Active {
				dot = ActiveMarker.Render("●")
				switch p.State(now) {
				case "on":
					tag = ActiveMarker.Render("ACTIVE")
				case "expired_fallback":
					tag = BadgePro.Render("ACTIVE but EXPIRED → direct")
				default:
					tag = BadgePro.Render("ACTIVE but EXPIRED → agy blocked")
				}
			}

			row := fmt.Sprintf("%s %s  %s  %s", dot, p.Name, DimStyle.Render(p.RawURL(true)), tag)
			if isCursor {
				sb.WriteString(cursorStr + SelectedStyle.Render(row) + "\n")
			} else {
				sb.WriteString(cursorStr + NormalStyle.Render(row) + "\n")
			}

			probeLine := FormatProbeLine(m.checks[p.Name], m.probing[p.Name], now)
			exp := util.FormatExpiry(p.ExpiresAt, now)
			sb.WriteString(fmt.Sprintf("      %s  %s\n", probeLine, DimStyle.Render("· expires "+exp)))
		}
	}

	switch m.mode {
	case proxyModeConfirmDelete:
		sb.WriteString("\n " + BadgePro.Render(fmt.Sprintf("delete '%s'? [y/N]", m.confirm)) + "\n")
	case proxyModeRename:
		sb.WriteString("\n " + CursorStyle.Render("rename '"+m.proxies[m.cursor].Name+"' ❯ ") + m.rename + "_\n")
	default:
		if m.status != "" {
			sb.WriteString("\n " + m.status + "\n")
		}
	}

	sb.WriteString(HelpStyle.Render("↑/↓ navigate • Enter enable & quit • space toggle • a add • e edit • R rename • d delete • r probe • q quit\n"))
	return sb.String()
}

func (m ProxyModel) viewEdit() string {
	var sb strings.Builder
	sb.WriteString(TitleStyle.Render(formatTitle("Edit Proxy '"+m.edit.origName+"'")) + "\n\n")
	for i, f := range m.edit.fields {
		cursorStr := "  "
		if i == m.edit.cursor {
			cursorStr = CursorStyle.Render("▸ ")
		}
		shown := f.value
		if f.name == "password" && shown != "" {
			shown = "****"
		} else if shown == "" {
			shown = DimStyle.Render("(empty)")
		}
		line := fmt.Sprintf("%-10s %s", f.name, shown)
		if i == m.edit.cursor {
			sb.WriteString(cursorStr + SelectedStyle.Render(line) + "\n")
		} else {
			sb.WriteString(cursorStr + NormalStyle.Render(line) + "\n")
		}
	}
	if m.edit.editing {
		f := m.edit.fields[m.edit.cursor]
		shown := m.edit.input
		if f.name == "password" {
			shown = strings.Repeat("*", utf8.RuneCountInString(m.edit.input))
		}
		sb.WriteString("\n " + CursorStyle.Render(f.name+" ❯ ") + shown + "_\n")
	}
	if m.edit.errMsg != "" {
		sb.WriteString("\n " + BadgeUltra.Render(m.edit.errMsg) + "\n")
	}
	sb.WriteString(HelpStyle.Render("↑/↓ pick field • Enter edit • d save • q discard\n"))
	return sb.String()
}

func (m ProxyModel) viewAdd() string {
	var sb strings.Builder
	sb.WriteString(TitleStyle.Render(formatTitle("Proxy Setup")) + "\n\n")
	if m.add.detected {
		sb.WriteString(" " + ActiveMarker.Render("✓") + " detected proxy in environment: " + m.add.cfg.RawURL(true) + "\n\n")
	}
	labels := []struct {
		prompt string
		cur    string
		secret bool
	}{
		{"1/7 server", m.add.cfg.Host, false},
		{"2/7 port", strconv.Itoa(m.add.cfg.Port), false},
		{"3/7 username (empty = no auth)", m.add.cfg.Username, false},
		{"4/7 password", m.add.cfg.Password, true},
		{"5/7 protocol (empty = auto-detect)", m.add.cfg.Protocol, false},
		{"6/7 validity (30d / 12h / YYYY-MM-DD, empty = never)", m.add.validity, false},
		{"7/7 fall back to DIRECT when expired? (y/N)", m.add.fallback, false},
	}
	if m.add.step < len(labels) {
		l := labels[m.add.step]
		hint := l.cur
		if l.secret && hint != "" {
			hint = "****"
		}
		if hint != "" {
			hint = " " + DimStyle.Render("["+hint+"]")
		}
		shown := m.add.input
		if l.secret {
			shown = strings.Repeat("*", utf8.RuneCountInString(m.add.input))
		}
		sb.WriteString(fmt.Sprintf(" %s%s ❯ %s_\n", l.prompt, hint, shown))
		if m.add.racing {
			sb.WriteString(" " + DimStyle.Render("detecting protocol (racing https / socks5h / http)…") + "\n")
		}
	} else if m.add.step == 7 {
		sb.WriteString(fmt.Sprintf(" '%s' already points to %s:%d — update it in place? [Y/n] %s_\n",
			m.add.dupeName, m.add.cfg.Host, m.add.cfg.Port, m.add.input))
	} else {
		expStr := "never"
		if m.add.cfg.ExpiresAt > 0 {
			expStr = time.Unix(m.add.cfg.ExpiresAt, 0).Format("2006-01-02 15:04")
		}
		fbStr := "refuse to run agy"
		if m.add.cfg.Fallback {
			fbStr = "fall back to direct"
		}
		sb.WriteString(" summary:\n")
		sb.WriteString(fmt.Sprintf("   name       %s\n", m.add.cfg.Name))
		sb.WriteString(fmt.Sprintf("   proxy      %s\n", m.add.cfg.RawURL(true)))
		sb.WriteString(fmt.Sprintf("   expires    %s\n", expStr))
		sb.WriteString(fmt.Sprintf("   on expiry  %s\n\n", fbStr))
		sb.WriteString(" save and enable? [Y/n] " + m.add.input + "_\n")
	}
	if m.add.errMsg != "" {
		sb.WriteString("\n " + BadgeUltra.Render(m.add.errMsg) + "\n")
	}
	sb.WriteString(HelpStyle.Render("Enter confirm • Esc cancel\n"))
	return sb.String()
}

func RunProxyManager(ps *proxy.Store) error {
	_, err := RunProxyManagerWithOptions(ps, ProxyOptions{})
	return err
}

func RunProxyManagerWithOptions(ps *proxy.Store, opts ProxyOptions) (string, error) {
	list, err := ps.List()
	if err != nil {
		return "", err
	}
	m := NewProxyModelWithOptions(list, ps, opts)
	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}
	return finalModel.(ProxyModel).ExitMessage(), nil
}
