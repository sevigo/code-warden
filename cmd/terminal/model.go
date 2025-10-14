package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sevigo/code-warden/internal/app"
	"github.com/sevigo/code-warden/internal/storage"
)

const asciiLogo = `
╔═════════════════════════════════════════════════════════════════════════════════════════════════╗
║                                                                                                 ║
║       ██████╗ ██████╗ ██████╗ ███████╗   ██╗    ██╗ █████╗ ██████╗ ██████╗ ███████╗███╗   ██╗   ║
║      ██╔════╝██╔═══██╗██╔══██╗██╔════╝   ██║    ██║██╔══██╗██╔══██╗██╔══██╗██╔════╝████╗  ██║   ║
║      ██║     ██║   ██║██║  ██║█████╗     ██║ █╗ ██║███████║██████╔╝██║  ██║█████╗  ██╔██╗ ██║   ║
║      ██║     ██║   ██║██║  ██║██╔══╝     ██║███╗██║██╔══██║██╔══██╗██║  ██║██╔══╝  ██║╚██╗██║   ║
║      ╚██████╗╚██████╔╝██████╔╝███████╗   ╚███╔███╔╝██║  ██║██║  ██║██████╔╝███████╗██║ ╚████║   ║
║       ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝    ╚══╝╚══╝ ╚═╝  ╚═╝╚═╝  ╚═╝╚═════╝ ╚══════╝╚═╝  ╚═══╝   ║
║                                                                                                 ║
║                                AI-POWERED CODE GUARDIAN v1.0.                                   ║
║                                                                                                 ║
╚═════════════════════════════════════════════════════════════════════════════════════════════════╝
`

type model struct {
	styles styles
	app    *app.App

	// UI Components
	viewport  viewport.Model
	textarea  textarea.Model
	spinner   spinner.Model
	progress  progress.Model
	isLoading bool

	// Session State
	repoPath       string
	repoFullName   string // Store the full name for status display
	collectionName string
	isScanned      bool
	history        []string
	showLogo       bool

	availableRepos []*storage.Repository
}

func initialModel(theme ThemeName) *model {
	styles := GetTheme(theme)
	ta := textarea.New()
	ta.Placeholder = "Enter command or ask about code..."
	ta.Focus()
	ta.Prompt = styles.prompt.Render("► ")
	ta.CharLimit = 500
	ta.SetWidth(50)
	ta.SetHeight(1)
	ta.ShowLineNumbers = false

	sp := spinner.New()
	sp.Spinner = spinner.Points
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	pr := progress.New(progress.WithDefaultGradient())

	return &model{
		styles:    styles,
		textarea:  ta,
		spinner:   sp,
		progress:  pr,
		isLoading: true,
		showLogo:  true,
		history:   []string{styles.ascii.Render(asciiLogo), "", "⚙ INITIALIZING CODE-WARDEN NEURAL NETWORK..."},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(initializeAppCmd(), m.spinner.Tick)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)
	m.spinner, spCmd = m.spinner.Update(msg)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}

			m.textarea.Reset()
			return m, m.processCommand(input)
		}

	case appInitializedMsg:
		m.isLoading = false
		if msg.err != nil {
			fmt.Fprintf(os.Stderr, "ERROR initializing app: %v\n", msg.err)
			m.history = append(m.history, "", m.styles.error.Render(msg.err.Error()))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return m, nil
		}
		m.app = msg.app
		// After app is initialized, immediately load the repositories.
		return m, loadReposCmd(m.app)

	case reposLoadedMsg:
		if msg.err != nil {
			m.history = append(m.history, "", m.styles.error.Render("Could not load repositories: "+msg.err.Error()))
		} else {
			m.availableRepos = msg.repos
			m.history = append(m.history, "", m.styles.success.Render("✓ SYSTEM ONLINE"))

			if len(m.availableRepos) == 1 {
				// Automatically select the only available repository.
				autoSelectCmd := fmt.Sprintf("/select %s", m.availableRepos[0].FullName)
				m.history = append(m.history, "", m.styles.command.Render(fmt.Sprintf("→ Automatically selecting the only available repository: %s", m.availableRepos[0].FullName)))
				m.viewport.SetContent(strings.Join(m.history, "\n"))
				m.viewport.GotoBottom()
				return m, m.processCommand(autoSelectCmd)
			} else if len(m.availableRepos) > 1 {
				m.history = append(m.history, "", m.styles.inactive.Render(fmt.Sprintf("%d repositories found. Use '/list' to see them or '/select [name]' to select one.", len(m.availableRepos))))
			}
		}
		m.history = append(m.history, "", "Type /help for commands or ask a question about your code.")
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case repoAddedMsg:
		m.isLoading = false
		if msg.err != nil {
			// Check for "already registered" error specifically
			if strings.Contains(msg.err.Error(), "is already registered") {
				m.history = append(m.history, "", m.styles.inactive.Render("INFO: "+msg.err.Error()))
			} else {
				m.history = append(m.history, "", m.styles.error.Render("ERROR: "+msg.err.Error()))
			}
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return m, loadReposCmd(m.app) // Reload repos but do not scan
		}

		// On successful add, immediately trigger a scan.
		successMsg := fmt.Sprintf("✅ REPO REGISTERED: %s", msg.repoFullName)
		m.history = append(m.history, "", m.styles.success.Render(successMsg), m.styles.command.Render("→ Starting initial scan... (this may take a while)"))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		m.isLoading = true
		// Call scanRepoCmd with force=true for initial scan
		return m, tea.Batch(m.spinner.Tick, scanRepoCmd(m.app, msg.repoPath, msg.repoFullName, true))

	case scanCompleteMsg:
		m.isLoading = false
		if msg.err != nil {
			fmt.Fprintf(os.Stderr, "SCAN FAILED: %v\n", msg.err)
			m.history = append(m.history, "", m.styles.error.Render("SCAN FAILED: "+msg.err.Error()))
		} else {
			m.isScanned = true
			m.repoPath = msg.repoPath
			m.repoFullName = msg.repoFullName
			m.collectionName = msg.collectionName
			m.history = append(m.history, "", m.styles.success.Render(fmt.Sprintf("✓ REPO INDEXED: %s", msg.repoFullName)))
			// Automatically select the newly scanned repo
			m.history = append(m.history, m.styles.command.Render(fmt.Sprintf("→ Context automatically set to %s. Ready for questions.", msg.repoFullName)))
		}
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		// After a scan, reload the repo list to reflect new state.
		return m, loadReposCmd(m.app)

	case answerChunkMsg:
		m.isLoading = false
		m.history[len(m.history)-1] += string(msg)
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()

	case errorMsg:
		m.isLoading = false
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", msg.err)
		m.history = append(m.history, "", m.styles.error.Render("⚠ "+msg.err.Error()))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case tea.WindowSizeMsg:
		m.styles.header.Width(msg.Width - 4)
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 10
		m.textarea.SetWidth(msg.Width - 10)
		m.viewport.SetContent(strings.Join(m.history, "\n"))
	}

	return m, tea.Batch(tiCmd, vpCmd, spCmd)
}

func (m *model) View() string {
	if m.app == nil {
		return fmt.Sprintf("\n  %s BOOTING SYSTEM...\n\n", m.spinner.View())
	}

	var statusParts []string
	if m.repoFullName != "" {
		statusParts = append(statusParts, fmt.Sprintf("REPO: %s", m.repoFullName))
	} else {
		statusParts = append(statusParts, "REPO: None Selected")
	}

	if m.isScanned {
		statusParts = append(statusParts, m.styles.success.Render("● INDEXED"))
	} else {
		statusParts = append(statusParts, m.styles.inactive.Render("○ NOT INDEXED"))
	}

	if m.app != nil && m.app.Cfg != nil {
		llmProvider := m.app.Cfg.LLMProvider
		embedProvider := m.app.Cfg.EmbedderProvider
		statusParts = append(statusParts, fmt.Sprintf("🤖 %s (%s)", m.app.Cfg.GeneratorModelName, llmProvider))
		statusParts = append(statusParts, fmt.Sprintf("⚙️ %s (%s)", m.app.Cfg.EmbedderModelName, embedProvider))
	}

	if m.isScanned && m.repoFullName != "" {
		var currentRepo *storage.Repository
		for _, r := range m.availableRepos {
			if r.FullName == m.repoFullName {
				currentRepo = r
				break
			}
		}
		if currentRepo != nil && len(currentRepo.LastIndexedSHA) >= 7 {
			statusParts = append(statusParts, fmt.Sprintf("COMMIT: %s", currentRepo.LastIndexedSHA[:7]))
		}
	}

	status := m.styles.inactive.Render(strings.Join(statusParts, " │ "))

	var loadingIndicator string
	if m.isLoading {
		loadingIndicator = " " + m.spinner.View() + " " + m.styles.success.Render("PROCESSING...")
	}

	return m.styles.app.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			m.styles.viewport.Render(m.viewport.View()),
			"",
			m.styles.footer.Render(
				lipgloss.JoinHorizontal(lipgloss.Left,
					m.textarea.View(),
					loadingIndicator,
				),
			),
			status,
		),
	)
}

func (m *model) processCommand(input string) tea.Cmd {
	m.history = append(m.history, m.styles.prompt.Render("► ")+input)
	m.viewport.SetContent(strings.Join(m.history, "\n"))
	m.viewport.GotoBottom()

	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}
	command := parts[0]
	args := parts[1:]

	switch command {
	case "/select":
		if len(args) != 1 {
			m.history = append(m.history, m.styles.error.Render("USAGE: /select [name]"))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		repoName := args[0]
		var foundRepo *storage.Repository
		for i := range m.availableRepos {
			if m.availableRepos[i].FullName == repoName {
				foundRepo = m.availableRepos[i]
				break
			}
		}

		if foundRepo == nil {
			m.history = append(m.history, m.styles.error.Render(fmt.Sprintf("Repository '%s' not found. Use /list to see available repositories.", repoName)))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}

		m.repoPath = foundRepo.ClonePath
		m.repoFullName = foundRepo.FullName
		m.collectionName = foundRepo.QdrantCollectionName
		m.isScanned = true // Assume if it's in the DB, it has been scanned at least once

		m.history = append(m.history, m.styles.success.Render(fmt.Sprintf("✓ Context set to repository: %s", repoName)))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return nil

	case "/list", "/ls":
		if len(m.availableRepos) == 0 {
			m.history = append(m.history, m.styles.inactive.Render("No repositories have been added yet. Use '/add [name] [path]' to get started."))
		} else {
			var b strings.Builder
			b.WriteString(m.styles.success.Render("AVAILABLE REPOSITORIES:"))
			for _, repo := range m.availableRepos {
				status := m.styles.inactive.Render("○ Not selected")
				if repo.FullName == m.repoFullName {
					status = m.styles.success.Render("● Selected")
				}
				b.WriteString(fmt.Sprintf("\n  - %s (%s) [%s]", m.styles.prompt.Render(repo.FullName), repo.ClonePath, status))
			}
			b.WriteString("\n\n" + m.styles.inactive.Render("Use '/select [name]' to switch context."))
			m.history = append(m.history, b.String())
		}
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return nil

	case "/add":
		if len(args) != 2 {
			m.history = append(m.history, "", m.styles.error.Render("USAGE: /add [name] [path_to_repo]"))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		m.isLoading = true
		m.history = append(m.history, "", m.styles.command.Render(fmt.Sprintf("→ Registering repository %s...", args[0])))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return tea.Batch(m.spinner.Tick, addRepoCmd(m.app, args[0], args[1]))

	case "/rescan":
		if len(args) != 1 {
			m.history = append(m.history, "", m.styles.error.Render("USAGE: /rescan [name]"))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		repoName := args[0]
		var foundRepo *storage.Repository
		for _, repo := range m.availableRepos {
			if repo.FullName == repoName {
				foundRepo = repo
				break
			}
		}

		if foundRepo == nil {
			m.history = append(m.history, m.styles.error.Render(fmt.Sprintf("Repository '%s' not found.", repoName)))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		m.isLoading = true
		m.history = append(m.history, "", m.styles.command.Render(fmt.Sprintf("→ Re-scanning repository %s for updates...", repoName)))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		// force=false for an incremental update
		return tea.Batch(m.spinner.Tick, scanRepoCmd(m.app, foundRepo.ClonePath, repoName, false))

	case "/question", "/q":
		if !m.isScanned {
			m.history = append(m.history, "", m.styles.error.Render("No repository is selected. Use '/list' and '/select [name]' first."))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		if len(args) < 1 {
			m.history = append(m.history, "", m.styles.error.Render("USAGE: /question [your question]"))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return nil
		}
		m.isLoading = true
		m.history = append(m.history, "", m.styles.command.Render("→ ANALYZING... "))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return tea.Batch(m.spinner.Tick, answerQuestionCmd(m.app, m.collectionName, strings.Join(args, " ")))

	case "/help", "/h":
		helpText := m.styles.success.Render("AVAILABLE COMMANDS:") + `

  /add [name] [path]   Register a local repository and scan it.
  /list, /ls           List all registered repositories.
  /select [name]       Set the active repository context for questions.
  /rescan [name]       Re-scan an existing repository for updates.
  /question [text]     Ask about the code (or just type your question).
  /help                Show this help message.
  /exit, /quit         Exit Code-Warden.

  ` + m.styles.inactive.Render("TIP: Once a repository is selected, you can ask questions directly without /question")
		m.history = append(m.history, "", helpText)
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return nil

	case "/exit", "/quit":
		return tea.Quit

	default:
		// Default action is to treat the input as a question if a repo is selected
		if m.isScanned {
			m.isLoading = true
			m.history = append(m.history, "", m.styles.command.Render("→ ANALYZING... "))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()
			return tea.Batch(m.spinner.Tick, answerQuestionCmd(m.app, m.collectionName, input))
		}
		// Otherwise, it's an unknown command
		m.history = append(m.history, "", m.styles.error.Render(fmt.Sprintf("UNKNOWN COMMAND: %s", command)), m.styles.inactive.Render("Type /help for assistance. If you want to ask a question, select a repository first with /select."))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()
		return nil
	}
}
