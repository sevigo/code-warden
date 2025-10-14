package main

import "github.com/charmbracelet/lipgloss"

type styles struct {
	app      lipgloss.Style
	header   lipgloss.Style
	viewport lipgloss.Style
	footer   lipgloss.Style
	inactive lipgloss.Style
	error    lipgloss.Style
	success  lipgloss.Style
	prompt   lipgloss.Style
	command  lipgloss.Style
	ascii    lipgloss.Style
}

type ThemeName string

const (
	ThemeMatrix    ThemeName = "matrix"
	ThemeAmber     ThemeName = "amber"
	ThemeCyberpunk ThemeName = "cyberpunk"
	ThemeIceBlue   ThemeName = "ice"
	ThemeDracula   ThemeName = "dracula"
	ThemeFire      ThemeName = "fire"
	ThemeCyan      ThemeName = "cyan"
)

type ThemePalette struct {
	Primary   lipgloss.Color
	Secondary lipgloss.Color
	Success   lipgloss.Color
	Warning   lipgloss.Color
	Error     lipgloss.Color
	Inactive  lipgloss.Color
}

var palettes = map[ThemeName]ThemePalette{
	ThemeCyan: {
		Primary:   lipgloss.Color("51"),
		Secondary: lipgloss.Color("33"),
		Success:   lipgloss.Color("46"),
		Warning:   lipgloss.Color("226"),
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeMatrix: {
		Primary:   lipgloss.Color("82"),  // brightGreen
		Secondary: lipgloss.Color("46"),  // green
		Success:   lipgloss.Color("82"),  // brightGreen
		Warning:   lipgloss.Color("190"), // lime
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeAmber: {
		Primary:   lipgloss.Color("220"), // brightAmber
		Secondary: lipgloss.Color("214"), // amber
		Success:   lipgloss.Color("220"), // brightAmber
		Warning:   lipgloss.Color("208"), // orange
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeCyberpunk: {
		Primary:   lipgloss.Color("201"), // magenta
		Secondary: lipgloss.Color("141"), // purple
		Success:   lipgloss.Color("51"),  // cyan
		Warning:   lipgloss.Color("213"), // pink
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeIceBlue: {
		Primary:   lipgloss.Color("159"), // ice
		Secondary: lipgloss.Color("39"),  // blue
		Success:   lipgloss.Color("51"),  // cyan
		Warning:   lipgloss.Color("159"), // ice
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeDracula: {
		Primary:   lipgloss.Color("141"), // purple
		Secondary: lipgloss.Color("117"), // cyan
		Success:   lipgloss.Color("84"),  // green
		Warning:   lipgloss.Color("212"), // pink
		Error:     lipgloss.Color("203"),
		Inactive:  lipgloss.Color("240"),
	},
	ThemeFire: {
		Primary:   lipgloss.Color("9"),   // brightRed
		Secondary: lipgloss.Color("196"), // red
		Success:   lipgloss.Color("226"), // yellow
		Warning:   lipgloss.Color("208"), // orange
		Error:     lipgloss.Color("196"),
		Inactive:  lipgloss.Color("240"),
	},
}

func GetTheme(theme ThemeName) styles {
	if palette, ok := palettes[theme]; ok {
		return newStylesFromPalette(palette)
	}
	return newStylesFromPalette(palettes[ThemeCyan])
}

func ListThemes() []ThemeName {
	return []ThemeName{
		ThemeCyan,
		ThemeMatrix,
		ThemeAmber,
		ThemeCyberpunk,
		ThemeIceBlue,
		ThemeDracula,
		ThemeFire,
	}
}

func newStylesFromPalette(p ThemePalette) styles {
	return styles{
		app: lipgloss.NewStyle().Margin(0, 1),
		header: lipgloss.NewStyle().
			Foreground(p.Primary).
			Bold(true).
			Border(lipgloss.DoubleBorder()).
			BorderForeground(p.Primary).
			Padding(0, 2).
			MarginBottom(1),
		viewport: lipgloss.NewStyle().
			PaddingLeft(1),
		footer: lipgloss.NewStyle().
			MarginTop(1).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(p.Primary).
			PaddingTop(1),
		inactive: lipgloss.NewStyle().Foreground(p.Inactive),
		error:    lipgloss.NewStyle().Foreground(p.Error).Bold(true),
		success:  lipgloss.NewStyle().Foreground(p.Success).Bold(true),
		prompt:   lipgloss.NewStyle().Foreground(p.Warning).Bold(true),
		command:  lipgloss.NewStyle().Foreground(p.Secondary).Italic(true),
		ascii:    lipgloss.NewStyle().Foreground(p.Primary).Bold(true),
	}
}
