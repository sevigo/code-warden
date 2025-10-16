package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
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
	styles    styles
	app       *app.App
	isLoading bool

	// UI Components
	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model
	renderer *glamour.TermRenderer

	// --- REFACTORED STATE MANAGEMENT ---
	availableRepos      []*storage.Repository
	selectedRepo        *storage.Repository
	history             []string
	conversationHistory []string
}

func initialModel(theme ThemeName) *model {
	styles := GetTheme(theme)
	ta := textarea.New()
	ta.Placeholder = "Enter a command or ask a question..."
	ta.Focus()
	ta.Prompt = styles.prompt.Render("► ")
	ta.SetHeight(1)
	ta.ShowLineNumbers = false

	sp := spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("51"))))

	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		fmt.Println("Error creating markdown renderer:", err)
		os.Exit(1)
	}

	return &model{
		styles:    styles,
		textarea:  ta,
		spinner:   sp,
		isLoading: true,
		renderer:  renderer,
		history:   []string{styles.ascii.Render(asciiLogo), "", "⚙ INITIALIZING CODE-WARDEN NEURAL NETWORK..."},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(initializeAppCmd(), m.spinner.Tick)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	m.spinner, cmd = m.spinner.Update(msg)
	cmds = append(cmds, cmd)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			input := strings.TrimSpace(m.textarea.Value())
			if input != "" {
				m.textarea.Reset()
				return m, m.processCommand(input)
			}
		}

	case appInitializedMsg:
		m.isLoading = false
		if msg.err != nil {
			m.history = append(m.history, m.styles.error.Render(msg.err.Error()))
		} else {
			m.app = msg.app
			return m, loadReposCmd(m.app)
		}

	case reposLoadedMsg:
		m.availableRepos = msg.repos
		if msg.err != nil {
			m.history = append(m.history, m.styles.error.Render("Could not load repositories: "+msg.err.Error()))
		} else {
			if !m.isLoading {
				m.history = append(m.history, m.styles.success.Render("✓ SYSTEM ONLINE"))
			}
			if len(m.availableRepos) == 1 && m.selectedRepo == nil {
				return m, m.processCommand(fmt.Sprintf("/select %s", m.availableRepos[0].FullName))
			}
		}
		m.history = append(m.history, m.styles.inactive.Render("Type /help for commands."))

	case repoAddedMsg:
		m.isLoading = true // Start loading for the scan
		if msg.err != nil {
			m.isLoading = false
			m.history = append(m.history, m.styles.inactive.Render("INFO: "+msg.err.Error()))
			return m, loadReposCmd(m.app)
		}
		m.history = append(m.history, m.styles.success.Render(fmt.Sprintf("✅ REPO REGISTERED: %s", msg.repoFullName)), m.styles.command.Render("→ Starting initial scan..."))
		return m, tea.Batch(m.spinner.Tick, scanRepoCmd(m.app, msg.repoPath, msg.repoFullName, true))

	case scanCompleteMsg:
		m.isLoading = false
		if msg.err != nil {
			m.history = append(m.history, m.styles.error.Render("SCAN FAILED: "+msg.err.Error()))
		} else {
			m.history = append(m.history, m.styles.success.Render(fmt.Sprintf("✓ REPO INDEXED: %s", msg.repoFullName)))
			return m, tea.Batch(loadReposCmd(m.app), func() tea.Msg {
				return tea.KeyMsg{Type: tea.KeyEnter, Runes: []rune(fmt.Sprintf("/select %s", msg.repoFullName))}
			})
		}

	case answerCompleteMsg:
		m.isLoading = false
		formattedAnswer, err := m.renderer.Render(msg.content)
		if err != nil {
			formattedAnswer = msg.content
		}

		m.history[len(m.history)-1] = formattedAnswer
		m.conversationHistory = append(m.conversationHistory, fmt.Sprintf("AI: %s", msg.content))

	case errorMsg:
		m.isLoading = false
		m.history = append(m.history, m.styles.error.Render("⚠ "+msg.err.Error()))

	case tea.WindowSizeMsg:
		m.viewport.Width = msg.Width - 2
		m.viewport.Height = msg.Height - 8
		m.textarea.SetWidth(msg.Width - 2)
	}

	m.viewport.SetContent(strings.Join(m.history, "\n"))
	m.viewport.GotoBottom()
	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.app == nil {
		return fmt.Sprintf("\n  %s BOOTING SYSTEM...\n\n", m.spinner.View())
	}

	var statusParts []string
	if m.selectedRepo != nil {
		statusParts = append(statusParts, fmt.Sprintf("REPO: %s", m.selectedRepo.FullName))
		if len(m.selectedRepo.LastIndexedSHA) >= 7 {
			statusParts = append(statusParts, fmt.Sprintf("COMMIT: %s", m.selectedRepo.LastIndexedSHA[:7]))
		}
	} else {
		statusParts = append(statusParts, "REPO: None Selected")
	}
	status := m.styles.inactive.Render(strings.Join(statusParts, " │ "))

	loadingIndicator := ""
	if m.isLoading {
		loadingIndicator = " " + m.spinner.View() + " " + m.styles.success.Render("PROCESSING...")
	}

	return m.styles.app.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			m.viewport.View(),
			"",
			m.textarea.View(),
			lipgloss.JoinHorizontal(lipgloss.Left, status, loadingIndicator),
		),
	)
}

func (m *model) processCommand(input string) tea.Cmd {
	m.history = append(m.history, m.styles.prompt.Render("► ")+input)

	parts := strings.Fields(input)
	command := parts[0]
	args := parts[1:]

	switch command {
	case "/add":
		if len(args) != 2 {
			m.history = append(m.history, m.styles.error.Render("USAGE: /add [name] [path_to_repo]"))
			return nil
		}
		m.isLoading = true
		m.history = append(m.history, m.styles.command.Render(fmt.Sprintf("→ Registering %s...", args[0])))
		return tea.Batch(m.spinner.Tick, addRepoCmd(m.app, args[0], args[1]))

	case "/list", "/ls":
		var b strings.Builder
		b.WriteString(m.styles.success.Render("AVAILABLE REPOSITORIES:"))
		for _, repo := range m.availableRepos {
			status := " "
			if m.selectedRepo != nil && repo.FullName == m.selectedRepo.FullName {
				status = m.styles.success.Render(" ●")
			}
			b.WriteString(fmt.Sprintf("\n  - %s (%s)%s", m.styles.prompt.Render(repo.FullName), repo.ClonePath, status))
		}
		m.history = append(m.history, b.String())
		return nil

	case "/select":
		if len(args) != 1 {
			m.history = append(m.history, m.styles.error.Render("USAGE: /select [name]"))
			return nil
		}
		for _, repo := range m.availableRepos {
			if repo.FullName == args[0] {
				m.selectedRepo = repo
				m.history = append(m.history, m.styles.success.Render(fmt.Sprintf("✓ Context set to: %s", args[0])))
				m.conversationHistory = nil // Reset history on repo switch
				return nil
			}
		}
		m.history = append(m.history, m.styles.error.Render(fmt.Sprintf("Repository '%s' not found.", args[0])))
		return nil

	case "/rescan":
		var repoName string
		if len(args) == 1 {
			repoName = args[0]
		} else if m.selectedRepo != nil {
			repoName = m.selectedRepo.FullName
		} else {
			m.history = append(m.history, m.styles.error.Render("USAGE: /rescan [name] or select a repo first"))
			return nil
		}

		for _, repo := range m.availableRepos {
			if repo.FullName == repoName {
				m.isLoading = true
				m.history = append(m.history, m.styles.command.Render(fmt.Sprintf("→ Re-scanning %s for updates...", repoName)))
				return tea.Batch(m.spinner.Tick, scanRepoCmd(m.app, repo.ClonePath, repoName, false))
			}
		}
		m.history = append(m.history, m.styles.error.Render(fmt.Sprintf("Repository '%s' not found.", repoName)))
		return nil

	case "/new", "/reset":
		m.conversationHistory = nil
		m.history = append(m.history, m.styles.inactive.Render("🧹 Conversation history cleared."))
		return nil

	case "/help", "/h":
		helpText := m.styles.success.Render("COMMANDS:") + `
  /add [name] [path]   Register & scan a local repository.
  /list, /ls           List all available repositories.
  /select [name]       Set the active repository for questions.
  /rescan [name?]      Re-scan a repo for updates (defaults to selected).
  /new                 Start a new conversation.
  /help                Show this help message.
  /exit, /quit         Exit the application.`
		m.history = append(m.history, helpText)
		return nil

	case "/exit", "/quit":
		return tea.Quit

	default: // Treat as a question
		if m.selectedRepo == nil {
			m.history = append(m.history, m.styles.error.Render("No repository selected. Use /select [name] first."))
			return nil
		}
		question := input
		m.conversationHistory = append(m.conversationHistory, fmt.Sprintf("User: %s", question))
		m.isLoading = true
		m.history = append(m.history, m.styles.command.Render("→ ANALYZING... "))

		return tea.Batch(
			m.spinner.Tick,
			answerQuestionCmd(
				m.app,
				m.selectedRepo.QdrantCollectionName,
				m.selectedRepo.EmbedderModelName,
				question,
				m.conversationHistory,
			),
		)
	}
}
