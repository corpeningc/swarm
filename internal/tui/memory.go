package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/calebcorpening/swarm/internal/memory"
)

// Messages emitted by the memory editor. Workspace.Update intercepts these to
// drive its mode transitions.
type (
	MemorySavedMsg    struct{ Repo string }
	MemoryCanceledMsg struct{}
)

// MemoryModal is a full-screen editor for a repo's .swarm/memory.md — the
// per-repo project memory swarm injects into every fresh spawn. Surfacing it
// in the TUI makes the otherwise-invisible memory editable in place.
type MemoryModal struct {
	repo string
	ta   textarea.Model
}

func NewMemoryModal(repo string) MemoryModal {
	ta := textarea.New()
	ta.Placeholder = "stable conventions for this repo — naming, test patterns, architecture…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Drop the default per-line "┃ " prompt — otherwise it draws a tall
	// vertical bar down every empty row. Set before SetWidth.
	ta.Prompt = ""
	ta.SetValue(memory.Read(repo))
	ta.Focus()
	return MemoryModal{repo: repo, ta: ta}
}

func (m MemoryModal) Init() tea.Cmd { return textarea.Blink }

func (m MemoryModal) Update(msg tea.Msg) (MemoryModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ta.SetWidth(min(msg.Width-12, 100))
		m.ta.SetHeight(min(msg.Height-10, 16))
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, func() tea.Msg { return MemoryCanceledMsg{} }
		case "ctrl+s":
			repo, content := m.repo, m.ta.Value()
			return m, func() tea.Msg {
				if err := memory.Write(repo, content); err != nil {
					return spawnErrorMsg{Err: "memory: " + err.Error()}
				}
				return MemorySavedMsg{Repo: repo}
			}
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

func (m MemoryModal) View() string {
	parts := []string{
		modalTitle.Render("project memory") + modalHint.Render("  "+m.repo),
		modalHint.Render("injected into every fresh spawn in this repo"),
		"",
		m.ta.View(),
		"",
		modalHint.Render("ctrl+s save · esc cancel"),
	}
	return modalBorder.Render(strings.Join(parts, "\n"))
}
