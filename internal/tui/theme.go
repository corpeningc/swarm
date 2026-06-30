package tui

import "github.com/charmbracelet/lipgloss"

// theme.go is the single source of truth for swarm's visual language: a cool,
// dark, teal-accented palette. Every style in the TUI is built from the tokens
// here so colors never drift between components. We force termenv.TrueColor at
// startup (cmd/swarm/main.go), so hex values render exactly in the chrome —
// only the agent's own VT content downsamples to 256-color.
//
// The accent system is deliberately cool (teal/cyan) with ONE warm exception:
// "awaiting input" stays amber. Attention is the thing the user must never
// miss, and a warm badge pops against the otherwise-cool field far better than
// another shade of teal would.
var (
	// Backgrounds — a cool, low-saturation dark slate that reads calmer and
	// more terminal-native than the old purple-tinted base.
	colorBg       = lipgloss.Color("#14171d") // app canvas behind every pane
	colorChip     = lipgloss.Color("#1f5f6b") // filled deep-teal chip (active tab)
	colorAccentBg = lipgloss.Color("#56c5d0") // bright teal fill (attached badge)

	// Accents — teal/cyan family.
	colorAccent     = lipgloss.Color("#56c5d0") // primary teal: focus, keys, running
	colorAccentSoft = lipgloss.Color("#8be9f0") // brighter teal for emphasis
	colorAccentDeep = lipgloss.Color("#cffafe") // pale teal-white on filled chips

	// Text ramp — cool blue-grays.
	colorTextHi    = lipgloss.Color("#e6edf3") // primary text
	colorTextMid   = lipgloss.Color("#aab8c5") // secondary text
	colorTextDim   = lipgloss.Color("#7587a0") // labels, hints
	colorTextFaint = lipgloss.Color("#55657a") // rules, disabled, "…more"

	// Borders — subtle by default; teal when the pane holds focus.
	colorBorder      = lipgloss.Color("#2c333f")
	colorBorderFocus = colorAccent

	// Semantic.
	colorAwait  = lipgloss.Color("#e8c468") // amber — the one warm accent
	colorAdd    = lipgloss.Color("#7ee787") // diff additions
	colorDel    = lipgloss.Color("#f47067") // diff deletions / warnings
	colorOnDark = lipgloss.Color("#06222a") // near-black for text on teal fills
)

var (
	// base carries the workspace background; every other style inherits it so
	// no segment punches a black hole between colored runs.
	base = lipgloss.NewStyle().Background(colorBg)

	// Pane borders. paneBorder is the idle/unfocused frame; paneBorderFocus
	// highlights whichever pane currently receives input.
	paneBorder      = base.Border(lipgloss.RoundedBorder()).BorderForeground(colorBorder)
	paneBorderFocus = base.Border(lipgloss.RoundedBorder()).BorderForeground(colorBorderFocus)

	dim       = base.Foreground(colorTextFaint)
	statusBar = base.Foreground(colorTextMid).Padding(0, 1)

	// sidebarTitle is the small-caps section header above the session list.
	sidebarTitle = base.Foreground(colorAccent).Bold(true)
	rowFocus     = base.Foreground(colorAccent).Bold(true)
	rowDim       = base.Foreground(colorTextMid)
	repoTag      = base.Foreground(colorTextDim)
	awaitTag     = base.Foreground(colorAwait).Bold(true)
	runTag       = base.Foreground(colorAccent)

	toastBox  = base.Foreground(colorDel).Padding(0, 1)
	attachTag = base.Foreground(colorOnDark).Background(colorAccentBg).Bold(true).Padding(0, 1)

	// Tab bar atop the main pane: the active tab is a filled teal chip, the
	// others read as dim, clickable-looking labels.
	tabActive   = base.Foreground(colorAccentDeep).Background(colorChip).Bold(true).Padding(0, 2)
	tabInactive = base.Foreground(colorTextDim).Padding(0, 2)

	// Per-session diff stats in the sidebar.
	addStat = base.Foreground(colorAdd)
	delStat = base.Foreground(colorDel)

	// keybar is the context-sensitive shortcut strip along the bottom.
	keybar    = base.Foreground(colorTextDim)
	keybarKey = base.Foreground(colorAccent).Bold(true)

	// Modal styling. Modals paint the same slate background as the rest of the
	// app so they don't sit in a black box, and their child text styles inherit
	// it too. The accent border ties them into the teal language.
	modalBorder = base.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorderFocus).
			Padding(1, 2)
	modalTitle = base.Bold(true).Foreground(colorAccentSoft)
	modalLabel = base.Foreground(colorTextDim)
	modalPath  = base.Foreground(colorTextHi)
	modalHint  = base.Foreground(colorTextFaint).Italic(true)

	listFocusRow = base.Foreground(colorAccent).Bold(true)
	listDimRow   = base.Foreground(colorTextMid)

	// confirmBorder frames destructive y/n prompts — warm red to read as a
	// caution.
	confirmBorder = base.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDel).
			Padding(1, 2)
)
