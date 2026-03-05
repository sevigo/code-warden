package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

const version = "v0.8.0"

func (a *App) PrintConfigSummary() {
	var sb strings.Builder

	sb.WriteString("┌─────────────────────────────────────────────────────────────┐\n")
	sb.WriteString("│                    Code-Warden Server                       │\n")
	sb.WriteString(fmt.Sprintf("│                      Version: %-28s│\n", version))
	sb.WriteString("├─────────────────────────────────────────────────────────────┤\n")
	sb.WriteString("│ Configuration Summary:                                      │\n")
	sb.WriteString("│                                                             │\n")

	sb.WriteString("│ Server:                                                     │\n")
	sb.WriteString(fmt.Sprintf("│   Port: %-51s│\n", a.Cfg.Server.Port))
	sb.WriteString(fmt.Sprintf("│   Max Workers: %-43d│\n", a.Cfg.Server.MaxWorkers))
	sb.WriteString("│                                                             │\n")

	sb.WriteString("│ Database:                                                   │\n")
	sb.WriteString(fmt.Sprintf("│   Type: %-51s│\n", "PostgreSQL"))
	sb.WriteString(fmt.Sprintf("│   Host: %s:%-46d│\n", a.Cfg.Database.Host, a.Cfg.Database.Port))

	dbStatus := "✓ Connected"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if a.DB != nil {
		if err := a.DB.PingContext(ctx); err != nil {
			dbStatus = "✗ Disconnected"
		}
	} else {
		dbStatus = "✗ Not initialized"
	}
	sb.WriteString(fmt.Sprintf("│   Status: %-49s│\n", dbStatus))
	sb.WriteString("│                                                             │\n")

	sb.WriteString("│ AI Provider:                                                │\n")
	sb.WriteString(fmt.Sprintf("│   LLM Provider: %-42s│\n", a.Cfg.AI.LLMProvider))
	sb.WriteString(fmt.Sprintf("│   Generator Model: %-40s│\n", a.Cfg.AI.GeneratorModel))
	sb.WriteString(fmt.Sprintf("│   Embedder Model: %-41s│\n", a.Cfg.AI.EmbedderModel))
	sb.WriteString("│                                                             │\n")

	if a.Cfg.Agent.Enabled {
		sb.WriteString("│ Agent (Autonomous Implementation):                         │\n")
		sb.WriteString(fmt.Sprintf("│   Status: %-49s│\n", "✓ Enabled"))
		sb.WriteString(fmt.Sprintf("│   Provider: %-48s│\n", a.Cfg.Agent.Provider))
		sb.WriteString(fmt.Sprintf("│   Model: %-50s│\n", a.Cfg.Agent.Model))
		sb.WriteString(fmt.Sprintf("│   Timeout: %-49s│\n", a.Cfg.Agent.Timeout))
		sb.WriteString(fmt.Sprintf("│   Max Sessions: %-43d│\n", a.Cfg.Agent.MaxConcurrentSessions))
		sb.WriteString("│                                                             │\n")
	}

	sb.WriteString("│ GitHub App:                                                 │\n")
	sb.WriteString(fmt.Sprintf("│   App ID: %-49d│\n", a.Cfg.GitHub.AppID))

	githubStatus := "✓ Configured"
	if _, err := os.Stat(a.Cfg.GitHub.PrivateKeyPath); os.IsNotExist(err) {
		githubStatus = "✗ Private key not found"
	}
	sb.WriteString(fmt.Sprintf("│   Status: %-49s│\n", githubStatus))
	sb.WriteString("└─────────────────────────────────────────────────────────────┘\n")

	fmt.Println(sb.String())
}
