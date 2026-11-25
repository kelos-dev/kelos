package logview

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ANSI palette indexes, matching the session terminal UI.
const (
	colorRed    = "1"
	colorGreen  = "2"
	colorYellow = "3"
	colorCyan   = "6"
)

// Line tints of diffs, after Claude Code's dark and light themes. They
// need a 256-color or true-color terminal; others get colored text.
var (
	addedTint   = lipgloss.AdaptiveColor{Dark: "#225c2b", Light: "#69db7c"}
	removedTint = lipgloss.AdaptiveColor{Dark: "#7a2936", Light: "#ffa8b4"}
)

type styles struct {
	muted    lipgloss.Style
	thinking lipgloss.Style
	bullet   lipgloss.Style
	tool     lipgloss.Style
	added    lipgloss.Style
	removed  lipgloss.Style
	success  lipgloss.Style
	warning  lipgloss.Style
	failure  lipgloss.Style
	active   lipgloss.Style
	done     lipgloss.Style
	// tinted is set when added and removed lines are shown with a
	// background tint, which is then padded to the full line width.
	tinted bool
}

// detectProfile returns the color profile of w, at least ANSI: color
// output was requested, so a terminal that reports none still gets it.
func detectProfile(w io.Writer) termenv.Profile {
	profile := termenv.NewOutput(w, termenv.WithUnsafe()).ColorProfile()
	if profile == termenv.Ascii {
		return termenv.ANSI
	}
	return profile
}

// newStyles returns the renderer's styles for profile; termenv.Ascii makes
// every style the identity so the layout survives on its own.
func newStyles(w io.Writer, profile termenv.Profile) styles {
	renderer := lipgloss.NewRenderer(w)
	base := renderer.NewStyle()
	s := styles{
		muted: base, thinking: base, bullet: base, tool: base,
		added: base, removed: base, success: base, warning: base,
		failure: base, active: base, done: base,
	}
	renderer.SetColorProfile(profile)
	if profile == termenv.Ascii {
		return s
	}

	s.muted = base.Faint(true)
	s.thinking = base.Faint(true).Italic(true)
	s.bullet = base.Foreground(lipgloss.Color(colorCyan))
	s.tool = base.Bold(true)
	s.added = base.Foreground(lipgloss.Color(colorGreen))
	s.removed = base.Foreground(lipgloss.Color(colorRed))
	if profile == termenv.TrueColor || profile == termenv.ANSI256 {
		s.added = base.Background(addedTint)
		s.removed = base.Background(removedTint)
		s.tinted = true
	}
	s.success = base.Foreground(lipgloss.Color(colorGreen)).Bold(true)
	s.warning = base.Foreground(lipgloss.Color(colorYellow)).Bold(true)
	s.failure = base.Foreground(lipgloss.Color(colorRed)).Bold(true)
	s.active = base.Bold(true)
	s.done = base.Faint(true).Strikethrough(true)
	return s
}
