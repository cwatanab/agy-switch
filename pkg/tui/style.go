package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var Version string

func formatTitle(subtitle string) string {
	if v := strings.TrimPrefix(strings.TrimSpace(Version), "v"); v != "" {
		return fmt.Sprintf("✦ agy-switch v%s — %s", v, subtitle)
	}
	return fmt.Sprintf("✦ agy-switch — %s", subtitle)
}

var (
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			MarginBottom(1)

	CursorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#04B575"))

	SelectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#3C3836"))

	NormalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D5C4A1"))

	DimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#928374"))

	BadgePro = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FABD2F"))

	BadgeUltra = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FB4934"))

	BadgeFree = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#83A598"))

	ActiveMarker = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#B8BB26"))

	HelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#665C54")).
			MarginTop(1)
)
