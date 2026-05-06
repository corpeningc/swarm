package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Messages emitted by the new-session modal. Workspace.Update intercepts these
// to drive its mode transitions; the modal itself is otherwise self-contained.
type (
	NewSessionSubmittedMsg struct {
		Repo   string
		Prompt string
	}
	NewSessionCanceledMsg struct{}
	BrowseRequestedMsg    struct{}
)

// NewSessionModal collects a repo path and prompt before spawning a session.
// Repo defaults to the current repo (set by Workspace at construction); the
// user can press Ctrl+B to swap it via the directory picker.
type NewSessionModal struct {
	repo   string
	prompt textinput.Model
	width  int
}

func NewSessionModalFor(repo string) NewSessionModal {
	ti := textinput.New()
	ti.Placeholder = "what should the agent do?"
	ti.Focus()
	ti.CharLimit = 0
	ti.Width = 60
	return NewSessionModal{repo: repo, prompt: ti}
}

// SetRepo updates the displayed repo path. Called by Workspace after the
// directory picker resolves a new path.
func (m *NewSessionModal) SetRepo(repo string) { m.repo = repo }

func (m NewSessionModal) Init() tea.Cmd { return textinput.Blink }

func (m NewSessionModal) Update(msg tea.Msg) (NewSessionModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.prompt.Width = min(msg.Width-8, 80)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return NewSessionCanceledMsg{} }
		case "ctrl+b":
			return m, func() tea.Msg { return BrowseRequestedMsg{} }
		case "enter":
			p := strings.TrimSpace(m.prompt.Value())
			if p == "" {
				// Don't submit empty prompts; let the user keep typing.
				return m, nil
			}
			return m, func() tea.Msg {
				return NewSessionSubmittedMsg{Repo: m.repo, Prompt: p}
			}
		}
	}
	var cmd tea.Cmd
	m.prompt, cmd = m.prompt.Update(msg)
	return m, cmd
}

var (
	modalBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)
	modalTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	modalLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	modalPath  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	modalHint  = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true)
)

func (m NewSessionModal) View() string {
	body := strings.Join([]string{
		modalTitle.Render("new session"),
		"",
		modalLabel.Render("repo  ") + modalPath.Render(m.repo),
		modalLabel.Render("      ") + modalHint.Render("ctrl+b to pick a different repo"),
		"",
		modalLabel.Render("prompt"),
		m.prompt.View(),
		"",
		modalHint.Render("enter submit · esc cancel"),
	}, "\n")
	return modalBorder.Render(body)
}
