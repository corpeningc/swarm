package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/calebcorpening/swarm/internal/worktree"
)

// Messages emitted by the new-session modal. Workspace.Update intercepts these
// to drive its mode transitions; the modal itself is otherwise self-contained.
type (
	NewSessionSubmittedMsg struct {
		Repo   string
		Prompt string
		Name   string // user-supplied; slugified by the workspace before use
	}
	NewSessionCanceledMsg struct{}
	BrowseRequestedMsg    struct{}
)

const (
	fieldName   = 0
	fieldPrompt = 1
	fieldList   = 2
)

// NewSessionModal collects a repo path, optional label, and prompt before
// spawning a session. Tab cycles between name, prompt, and the existing-
// worktree list.
type NewSessionModal struct {
	repo     string
	name     textinput.Model
	prompt   textinput.Model
	focusIdx int
	width    int

	// existingWorktrees lists the slug names of worktrees already on disk
	// in the chosen repo. Refreshed when SetRepo is called.
	existingWorktrees []string
	listCursor        int
}

func NewSessionModalFor(repo string) NewSessionModal {
	name := textinput.New()
	name.Placeholder = "name (slug → worktree dir; existing names reattach)"
	name.CharLimit = 40
	name.Width = 60
	name.Focus()

	prompt := textinput.New()
	prompt.Placeholder = "what should the agent do?"
	prompt.CharLimit = 0
	prompt.Width = 60

	m := NewSessionModal{
		repo:     repo,
		name:     name,
		prompt:   prompt,
		focusIdx: fieldName,
	}
	m.refreshWorktrees()
	return m
}

// SetRepo updates the displayed repo path and rescans existing worktrees.
func (m *NewSessionModal) SetRepo(repo string) {
	m.repo = repo
	m.refreshWorktrees()
	m.listCursor = 0
}

func (m *NewSessionModal) refreshWorktrees() {
	m.existingWorktrees = nil
	if m.repo == "" {
		return
	}
	entries, err := os.ReadDir(worktree.SwarmWorktreesDir(m.repo))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			m.existingWorktrees = append(m.existingWorktrees, e.Name())
		}
	}
	sort.Strings(m.existingWorktrees)
}

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
		case "up":
			if m.focusIdx == fieldList && len(m.existingWorktrees) > 0 {
				m.listCursor = (m.listCursor - 1 + len(m.existingWorktrees)) % len(m.existingWorktrees)
				return m, nil
			}
		case "down":
			if m.focusIdx == fieldList && len(m.existingWorktrees) > 0 {
				m.listCursor = (m.listCursor + 1) % len(m.existingWorktrees)
				return m, nil
			}
		case "enter":
			// Enter on the worktree list: copy the highlighted name into
			// the name field and bounce focus to prompt so the user can
			// type a follow-up turn.
			if m.focusIdx == fieldList && len(m.existingWorktrees) > 0 {
				m.name.SetValue(m.existingWorktrees[m.listCursor])
				m.focusIdx = fieldPrompt
				m.name.Blur()
				m.prompt.Focus()
				return m, nil
			}
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
	switch m.focusIdx {
	case fieldName:
		m.name, cmd = m.name.Update(msg)
	case fieldPrompt:
		m.prompt, cmd = m.prompt.Update(msg)
	}
	return m, cmd
}

func (m *NewSessionModal) cycleFocus(delta int) {
	// Skip the worktree list focus target when there's nothing to pick.
	maxField := fieldPrompt
	if len(m.existingWorktrees) > 0 {
		maxField = fieldList
	}
	span := maxField + 1
	m.focusIdx = (m.focusIdx + delta + span) % span
	switch m.focusIdx {
	case fieldName:
		m.name.Focus()
		m.prompt.Blur()
	case fieldPrompt:
		m.name.Blur()
		m.prompt.Focus()
	case fieldList:
		m.name.Blur()
		m.prompt.Blur()
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

	listFocusRow = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	listDimRow   = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)

func (m NewSessionModal) View() string {
	parts := []string{
		modalTitle.Render("new session"),
		"",
		modalLabel.Render("repo  ") + modalPath.Render(m.repo),
		modalLabel.Render("      ") + modalHint.Render("ctrl+b to pick a different repo"),
		"",
		modalLabel.Render("name  "),
		m.name.View(),
	}

	if len(m.existingWorktrees) > 0 {
		parts = append(parts, "", modalLabel.Render("existing worktrees"))
		// Cap the visible list so the modal doesn't balloon.
		const maxVisible = 5
		visible := m.existingWorktrees
		startIdx := 0
		if len(visible) > maxVisible {
			// Window the list around the cursor.
			startIdx = m.listCursor - maxVisible/2
			if startIdx < 0 {
				startIdx = 0
			}
			if startIdx > len(visible)-maxVisible {
				startIdx = len(visible) - maxVisible
			}
			visible = visible[startIdx : startIdx+maxVisible]
		}
		for i, name := range visible {
			row := name
			actualIdx := startIdx + i
			if m.focusIdx == fieldList && actualIdx == m.listCursor {
				row = listFocusRow.Render("▎ " + row + "  ↩ pick")
			} else {
				row = listDimRow.Render("  " + row)
			}
			parts = append(parts, row)
		}
		if len(m.existingWorktrees) > maxVisible {
			parts = append(parts, modalHint.Render(filepath.Join("…", "and more above/below")))
		}
	}

	parts = append(parts,
		"",
		modalLabel.Render("prompt"),
		m.prompt.View(),
		"",
		modalHint.Render("tab next field · enter submit · esc cancel"),
	)
	return modalBorder.Render(strings.Join(parts, "\n"))
}
