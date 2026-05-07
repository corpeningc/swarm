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
		Name   string // optional user-supplied label
	}
	NewSessionCanceledMsg struct{}
	BrowseRequestedMsg    struct{}
)

const (
	fieldName   = 0
	fieldPrompt = 1
)

// NewSessionModal collects a repo path, optional label, and prompt before
// spawning a session. Tab cycles between name and prompt fields.
type NewSessionModal struct {
	repo     string
	name     textinput.Model
	prompt   textinput.Model
	focusIdx int
	width    int
}

func NewSessionModalFor(repo string) NewSessionModal {
	name := textinput.New()
	name.Placeholder = "label this session (optional)"
	name.CharLimit = 40
	name.Width = 50
	name.Focus()

	prompt := textinput.New()
	prompt.Placeholder = "what should the agent do?"
	prompt.CharLimit = 0
	prompt.Width = 60

	return NewSessionModal{
		repo:     repo,
		name:     name,
		prompt:   prompt,
		focusIdx: fieldName,
	}
}

// SetRepo updates the displayed repo path. Called by Workspace after the
// directory picker resolves a new path.
func (m *NewSessionModal) SetRepo(repo string) { m.repo = repo }

func (m NewSessionModal) Init() tea.Cmd { return textinput.Blink }

func (m NewSessionModal) Update(msg tea.Msg) (NewSessionModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.name.Width = min(msg.Width-12, 60)
		m.prompt.Width = min(msg.Width-12, 80)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return NewSessionCanceledMsg{} }
		case "ctrl+b":
			return m, func() tea.Msg { return BrowseRequestedMsg{} }
		case "tab":
			m.cycleFocus(+1)
			return m, nil
		case "shift+tab":
			m.cycleFocus(-1)
			return m, nil
		case "enter":
			p := strings.TrimSpace(m.prompt.Value())
			if p == "" {
				// Empty prompt: nudge focus to prompt field if we're on
				// name. Otherwise stay put — let the user keep typing.
				if m.focusIdx == fieldName {
					m.cycleFocus(+1)
				}
				return m, nil
			}
			return m, func() tea.Msg {
				return NewSessionSubmittedMsg{
					Repo:   m.repo,
					Prompt: p,
					Name:   strings.TrimSpace(m.name.Value()),
				}
			}
		}
	}
	var cmd tea.Cmd
	if m.focusIdx == fieldName {
		m.name, cmd = m.name.Update(msg)
	} else {
		m.prompt, cmd = m.prompt.Update(msg)
	}
	return m, cmd
}

func (m *NewSessionModal) cycleFocus(delta int) {
	m.focusIdx = (m.focusIdx + delta + 2) % 2
	if m.focusIdx == fieldName {
		m.name.Focus()
		m.prompt.Blur()
	} else {
		m.name.Blur()
		m.prompt.Focus()
	}
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
		modalLabel.Render("name  "),
		m.name.View(),
		"",
		modalLabel.Render("prompt"),
		m.prompt.View(),
		"",
		modalHint.Render("tab next field · enter submit · esc cancel"),
	}, "\n")
	return modalBorder.Render(body)
}
