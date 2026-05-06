// Package tui implements the Bubbletea-based workspace UI.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Workspace struct {
	width, height int
	quitting      bool
}

func NewWorkspace() Workspace {
	return Workspace{}
}

func (w Workspace) Init() tea.Cmd { return nil }

func (w Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = m.Width, m.Height
	case tea.KeyMsg:
		switch m.String() {
		case "q", "ctrl+c":
			w.quitting = true
			return w, tea.Quit
		}
	}
	return w, nil
}

var (
	border    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63"))
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusBar = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("237")).Padding(0, 1)
)

func (w Workspace) View() string {
	if w.quitting {
		return ""
	}
	if w.width == 0 {
		return "starting…"
	}

	sidebarW := 28
	mainW := w.width - sidebarW - 4
	bodyH := w.height - 3

	sidebar := border.Width(sidebarW).Height(bodyH).Render(
		"Sessions\n" + dim.Render(strings.Repeat("─", sidebarW-2)) + "\n" + dim.Render("(no sessions)\npress n to spawn"),
	)
	main := border.Width(mainW).Height(bodyH).Render(
		"Swarm v0.0.1-dev\n\n" + dim.Render("welcome. select or spawn a session to begin.\n\nq quit · n new · d diff · x kill"),
	)
	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)

	status := statusBar.Width(w.width).Render(fmt.Sprintf("%d sessions · $0.00 · %dx%d", 0, w.width, w.height))
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}
