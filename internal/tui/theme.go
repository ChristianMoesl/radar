package tui

import "github.com/charmbracelet/lipgloss"

// Catppuccin Mocha: https://catppuccin.com/palette/
// Keep the terminal's background; only selections paint their own background.
const (
	mochaText     = lipgloss.Color("#cdd6f4")
	mochaSubtext0 = lipgloss.Color("#a6adc8")
	mochaOverlay2 = lipgloss.Color("#9399b2")
	mochaSurface0 = lipgloss.Color("#313244")
	mochaSurface1 = lipgloss.Color("#45475a")
	mochaLavender = lipgloss.Color("#b4befe")
	mochaRed      = lipgloss.Color("#f38ba8")
	mochaYellow   = lipgloss.Color("#f9e2af")
	mochaBlue     = lipgloss.Color("#89b4fa")
	mochaGreen    = lipgloss.Color("#a6e3a1")
	mochaTeal     = lipgloss.Color("#94e2d5")
)

var (
	appStyle = lipgloss.NewStyle().Foreground(mochaText).Padding(2, 4)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(mochaSurface1).
			Padding(1, 2)

	textStyle      = lipgloss.NewStyle().Foreground(mochaText)
	titleStyle     = lipgloss.NewStyle().Foreground(mochaYellow).Bold(true)
	subtleStyle    = lipgloss.NewStyle().Foreground(mochaSubtext0)
	referenceStyle = lipgloss.NewStyle().Foreground(mochaOverlay2)
	errorStyle     = lipgloss.NewStyle().Foreground(mochaRed).Bold(true)
	helpStyle      = lipgloss.NewStyle().Foreground(mochaSubtext0)
	helpKeyStyle   = lipgloss.NewStyle().Foreground(mochaLavender)
	separatorStyle = lipgloss.NewStyle().Foreground(mochaSurface1)

	urgentStyle    = lipgloss.NewStyle().Foreground(mochaRed).Bold(true)
	attentionStyle = lipgloss.NewStyle().Foreground(mochaYellow).Bold(true)
	progressStyle  = lipgloss.NewStyle().Foreground(mochaBlue).Bold(true)
	doneStyle      = lipgloss.NewStyle().Foreground(mochaGreen).Bold(true)
	lowStyle       = lipgloss.NewStyle().Foreground(mochaSubtext0).Bold(true)

	selectedStyle = lipgloss.NewStyle().
			Foreground(mochaText).
			Background(mochaSurface0)

	notificationStyle = lipgloss.NewStyle().
				Foreground(mochaGreen).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(mochaTeal).
				Padding(0, 1)

	errorNotificationStyle = lipgloss.NewStyle().
				Foreground(mochaRed).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(mochaRed).
				Padding(0, 1)
)
