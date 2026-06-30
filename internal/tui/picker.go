package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/corpeningc/swarm/internal/worktree"
)

// PickerResultMsg is emitted when the user picks a directory and it
// validates as a git repo. RepoRoot is the resolved repository root, which
// may differ from the path the user selected if they picked a subdirectory.
type PickerResultMsg struct {
	RepoRoot string
}

// PickerCanceledMsg is emitted when the user backs out of the picker.
type PickerCanceledMsg struct{}

// PickerErrorMsg surfaces validation failures (e.g. selected path is not in
// a git repo) so the workspace can show a toast.
type PickerErrorMsg struct{ Err string }

// RepoPicker wraps bubbles/filepicker with directory-only navigation and
// git-repo validation. The selection is a directory; the result is the repo
// root we resolved from it.
type RepoPicker struct {
	fp     filepicker.Model
	width  int
	height int
}

func NewRepoPicker(startDir string) RepoPicker {
	fp := filepicker.New()
	fp.CurrentDirectory = startDir
	fp.DirAllowed = true
	fp.FileAllowed = false
	fp.ShowHidden = false
	fp.AutoHeight = false
	return RepoPicker{fp: fp}
}

func (p RepoPicker) Init() tea.Cmd { return p.fp.Init() }

func (p RepoPicker) Update(msg tea.Msg) (RepoPicker, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		p.fp.Height = msg.Height - 8
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return p, func() tea.Msg { return PickerCanceledMsg{} }
		}
	}

	var cmd tea.Cmd
	p.fp, cmd = p.fp.Update(msg)

	if didSelect, path := p.fp.DidSelectFile(msg); didSelect {
		return p, validateRepoCmd(path)
	}
	return p, cmd
}

func (p RepoPicker) View() string {
	body := strings.Join([]string{
		modalTitle.Render("pick a repo"),
		modalHint.Render(p.fp.CurrentDirectory),
		"",
		p.fp.View(),
		"",
		modalHint.Render("enter select · esc cancel"),
	}, "\n")
	return modalBorder.Render(body)
}

// validateRepoCmd resolves the picked path to a git repo root. Runs
// git rev-parse off the UI thread.
func validateRepoCmd(path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		root, err := worktree.FindRepoRoot(ctx, path)
		if err != nil {
			return PickerErrorMsg{Err: path + " is not inside a git repository"}
		}
		return PickerResultMsg{RepoRoot: root}
	}
}
