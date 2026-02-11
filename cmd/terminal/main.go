package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sevigo/code-warden/internal/config"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Printf("Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	slog.Info("Code-Warden terminal starting up")

	// Parse command-line flags
	themeFlag := flag.String("theme", "", "UI theme (cyan, matrix, amber, cyberpunk, ice, dracula, fire)")
	listThemes := flag.Bool("list-themes", false, "List all available themes")
	flag.Parse()

	// If user wants to list themes
	if *listThemes {
		fmt.Println("Available themes:")
		for _, theme := range ListThemes() {
			fmt.Printf("  - %s\n", theme)
		}
		os.Exit(0)
	}

	selectedTheme := *themeFlag
	if selectedTheme == "" {
		selectedTheme = os.Getenv("CODE_WARDEN_THEME")
	}
	if selectedTheme == "" && cfg.Server.Theme == "" {
		selectedTheme = "cyan"
	}

	theme := ThemeName(selectedTheme)
	validTheme := false
	for _, t := range ListThemes() {
		if t == theme {
			validTheme = true
			break
		}
	}
	if !validTheme {
		fmt.Printf("Invalid theme '%s'. Use --list-themes to see available options.\n", theme)
		os.Exit(1)
	}

	p := tea.NewProgram(initialModel(theme), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		slog.Error("error running program", "error", err)
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
	slog.Info("Code-Warden terminal shut down successfully")
}
