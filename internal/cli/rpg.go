package cli

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ANSI escape codes for terminal formatting.
const (
	ansiReset        = "\033[0m"
	ansiBold         = "\033[1m"
	ansiDim          = "\033[2m"
	ansiRed          = "\033[31m"
	ansiGreen        = "\033[32m"
	ansiYellow       = "\033[33m"
	ansiMagenta      = "\033[35m"
	ansiCyan         = "\033[36m"
	ansiWhite        = "\033[37m"
	ansiGray         = "\033[90m"
	ansiBrightRed    = "\033[91m"
	ansiBrightGreen  = "\033[92m"
	ansiBrightYellow = "\033[93m"
	ansiBrightCyan   = "\033[96m"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// RPGStage represents a stage in the quest.
type RPGStage int

const (
	StageCredentials RPGStage = iota
	StageWorkspace
	StageDispatch
	StageSummon
	StagePortal
	StageBattle
	StageResolution
	stageCount // sentinel
)

// StageStatus represents the current status of a quest stage.
type StageStatus int

const (
	StatusPending StageStatus = iota
	StatusActive
	StatusDone
	StatusSkipped
	StatusError
)

// stageInfo holds display information for a single quest stage.
type stageInfo struct {
	Name   string
	Status StageStatus
}

// BattleEntry represents a single entry in the battle log.
type BattleEntry struct {
	Type    string // "turn", "text", "tool", "status", "result", "error"
	Turn    int
	Tool    string
	Content string
}

// RPGState holds the mutable state for RPG visualization.
type RPGState struct {
	mu sync.Mutex

	Prompt   string
	HeroType string
	Realm    string
	TaskName string
	Model    string

	Stages [stageCount]stageInfo

	BattleLog   []BattleEntry
	StartTime   time.Time
	CurrentTurn int
	TotalCost   float64
	NumTurns    int

	SpinnerIdx int
	Finished   bool
	Success    bool
	Message    string
	Outputs    []string
}

// NewRPGState creates a new RPG visualization state.
func NewRPGState(prompt, heroType, realm, taskName, model string) *RPGState {
	s := &RPGState{
		Prompt:    prompt,
		HeroType:  heroType,
		Realm:     realm,
		TaskName:  taskName,
		Model:     model,
		StartTime: time.Now(),
	}
	s.Stages = [stageCount]stageInfo{
		{Name: "Forging credentials"},
		{Name: "Preparing workspace"},
		{Name: "Dispatching quest"},
		{Name: "Summoning hero"},
		{Name: "Opening portal"},
		{Name: "Hero battles the challenge"},
		{Name: "Quest resolution"},
	}
	return s
}

// SetStage updates a stage's status thread-safely.
func (s *RPGState) SetStage(stage RPGStage, status StageStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Stages[stage].Status = status
}

// AddBattle adds a battle log entry thread-safely.
func (s *RPGState) AddBattle(entry BattleEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.BattleLog = append(s.BattleLog, entry)
}

// AdvanceSpinner advances the spinner animation frame.
func (s *RPGState) AdvanceSpinner() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SpinnerIdx = (s.SpinnerIdx + 1) % len(spinnerFrames)
}

// SetFinished marks the quest as complete.
func (s *RPGState) SetFinished(success bool, message string, outputs []string, cost float64, turns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Finished = true
	s.Success = success
	s.Message = message
	s.Outputs = outputs
	if cost > 0 {
		s.TotalCost = cost
	}
	if turns > 0 {
		s.NumTurns = turns
	}
}

// IsFinished returns whether the quest is complete.
func (s *RPGState) IsFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Finished
}

// Render builds the complete terminal frame as a string.
func (s *RPGState) Render(width, height int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if width < 50 {
		width = 50
	}
	if height < 20 {
		height = 20
	}

	fw := width // frame width
	iw := fw - 4

	var b strings.Builder
	b.WriteString("\033[H") // move cursor home

	// Title block (5 lines)
	b.WriteString(rpgTopBorder(fw))
	b.WriteString(rpgEmptyLine(fw))
	b.WriteString(s.renderTitle(fw))
	b.WriteString(rpgEmptyLine(fw))
	b.WriteString(rpgMidBorder(fw))

	// Info block (3 lines)
	b.WriteString(s.renderQuestLine(fw, iw))
	b.WriteString(s.renderHeroLine(fw))
	b.WriteString(rpgMidBorder(fw))

	// Stages block (stageCount + 2 lines)
	b.WriteString(rpgEmptyLine(fw))
	for i := 0; i < int(stageCount); i++ {
		b.WriteString(s.renderStageLine(fw, i))
	}
	b.WriteString(rpgEmptyLine(fw))
	b.WriteString(rpgMidBorder(fw))

	// Battle log: header (1 line) + entries + bottom border (1 line)
	fixedLines := 5 + 3 + int(stageCount) + 2 + 1 + 1
	availLog := height - fixedLines
	if availLog < 1 {
		availLog = 1
	}

	b.WriteString(s.renderBattleHeader(fw))
	entries := s.lastEntries(availLog)
	for _, e := range entries {
		b.WriteString(s.renderBattleEntry(fw, iw, e))
	}
	for i := len(entries); i < availLog; i++ {
		b.WriteString(rpgEmptyLine(fw))
	}

	b.WriteString(rpgBottomBorder(fw))
	b.WriteString("\033[J") // clear to end of screen

	return b.String()
}

// --- Border helpers ---

func rpgTopBorder(fw int) string {
	return ansiGray + "╔" + strings.Repeat("═", fw-2) + "╗" + ansiReset + "\n"
}

func rpgMidBorder(fw int) string {
	return ansiGray + "╠" + strings.Repeat("═", fw-2) + "╣" + ansiReset + "\n"
}

func rpgBottomBorder(fw int) string {
	return ansiGray + "╚" + strings.Repeat("═", fw-2) + "╝" + ansiReset + "\n"
}

func rpgEmptyLine(fw int) string {
	return ansiGray + "║" + ansiReset + strings.Repeat(" ", fw-2) + ansiGray + "║" + ansiReset + "\n"
}

// rpgBorderedLine wraps content with box-drawing borders and right-pads to frameWidth.
func rpgBorderedLine(fw int, content string) string {
	vis := rpgVisibleWidth(content)
	pad := fw - 2 - vis
	if pad < 0 {
		pad = 0
	}
	return ansiGray + "║" + ansiReset + content + strings.Repeat(" ", pad) + ansiGray + "║" + ansiReset + "\n"
}

// --- Content renderers ---

func (s *RPGState) renderTitle(fw int) string {
	var title, color string
	if s.Finished {
		if s.Success {
			title = "⚔  Q U E S T   C O M P L E T E  ⚔"
			color = ansiBold + ansiBrightGreen
		} else {
			title = "✗  Q U E S T   F A I L E D  ✗"
			color = ansiBold + ansiBrightRed
		}
	} else {
		title = "⚔  A X O N   Q U E S T  ⚔"
		color = ansiBold + ansiBrightYellow
	}
	tLen := utf8.RuneCountInString(title)
	leftPad := (fw - 2 - tLen) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	content := strings.Repeat(" ", leftPad) + color + title + ansiReset
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) renderQuestLine(fw, iw int) string {
	maxLen := iw - 12
	if maxLen < 10 {
		maxLen = 10
	}
	p := s.Prompt
	if utf8.RuneCountInString(p) > maxLen {
		p = string([]rune(p)[:maxLen-3]) + "..."
	}
	content := fmt.Sprintf("  %sQuest%s  \"%s\"", ansiBold+ansiYellow, ansiReset, p)
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) renderHeroLine(fw int) string {
	elapsed := time.Since(s.StartTime).Truncate(time.Second)
	content := fmt.Sprintf("  %sHero%s %s%s%s    %sRealm%s %s%s%s    %sTime%s %s%s%s",
		ansiBold+ansiYellow, ansiReset, ansiCyan, s.HeroType, ansiReset,
		ansiBold+ansiYellow, ansiReset, ansiCyan, s.Realm, ansiReset,
		ansiBold+ansiYellow, ansiReset, ansiCyan, rpgFmtDuration(elapsed), ansiReset,
	)
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) renderStageLine(fw, idx int) string {
	st := s.Stages[idx]
	var icon, color string
	switch st.Status {
	case StatusDone:
		icon = "✓"
		color = ansiGreen
	case StatusActive:
		icon = spinnerFrames[s.SpinnerIdx]
		color = ansiBrightCyan + ansiBold
	case StatusError:
		icon = "✗"
		color = ansiBrightRed
	case StatusSkipped:
		icon = "−"
		color = ansiGray
	default:
		icon = "·"
		color = ansiGray
	}
	content := fmt.Sprintf("  %s[%s]%s %s%s%s", color, icon, ansiReset, color, st.Name, ansiReset)
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) renderBattleHeader(fw int) string {
	content := fmt.Sprintf("  %s%sBATTLE LOG%s", ansiBold, ansiYellow, ansiReset)
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) renderBattleEntry(fw, iw int, e BattleEntry) string {
	var content string
	switch e.Type {
	case "turn":
		line := fmt.Sprintf("── Turn %d ", e.Turn)
		fill := iw - 2 - utf8.RuneCountInString(line)
		if fill > 0 {
			line += strings.Repeat("─", fill)
		}
		content = fmt.Sprintf("  %s%s%s", ansiYellow, line, ansiReset)
	case "text":
		t := rpgFirstLine(e.Content)
		max := iw - 4
		if max < 10 {
			max = 10
		}
		if utf8.RuneCountInString(t) > max {
			t = string([]rune(t)[:max-3]) + "..."
		}
		content = fmt.Sprintf("    %s%s%s", ansiWhite, t, ansiReset)
	case "tool":
		icon := rpgToolIcon(e.Tool)
		summ := e.Content
		max := iw - 18
		if max < 10 {
			max = 10
		}
		if utf8.RuneCountInString(summ) > max {
			summ = string([]rune(summ)[:max-3]) + "..."
		}
		content = fmt.Sprintf("    %s%s %-8s%s %s%s%s",
			ansiBold+ansiMagenta, icon, e.Tool, ansiReset,
			ansiCyan, summ, ansiReset)
	case "status":
		t := e.Content
		max := iw - 4
		if max > 0 && utf8.RuneCountInString(t) > max {
			t = string([]rune(t)[:max-3]) + "..."
		}
		content = fmt.Sprintf("  %s%s%s", ansiGray, t, ansiReset)
	case "result":
		if strings.Contains(e.Content, "fail") || strings.Contains(e.Content, "error") {
			content = fmt.Sprintf("  %s%s%s", ansiBrightRed+ansiBold, e.Content, ansiReset)
		} else {
			content = fmt.Sprintf("  %s%s%s", ansiBrightGreen+ansiBold, e.Content, ansiReset)
		}
	case "error":
		content = fmt.Sprintf("  %s✗ %s%s", ansiBrightRed, e.Content, ansiReset)
	default:
		content = "  " + e.Content
	}
	return rpgBorderedLine(fw, content)
}

func (s *RPGState) lastEntries(n int) []BattleEntry {
	if len(s.BattleLog) <= n {
		return s.BattleLog
	}
	return s.BattleLog[len(s.BattleLog)-n:]
}

// --- Utility functions ---

// rpgVisibleWidth returns the visible width of a string, ignoring ANSI escapes.
func rpgVisibleWidth(s string) int {
	return utf8.RuneCountInString(ansiEscapeRegex.ReplaceAllString(s, ""))
}

// rpgFmtDuration formats a duration as "Xs" or "XmYYs".
func rpgFmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// rpgFirstLine returns the first line of a string.
func rpgFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// rpgToolIcon returns a Unicode icon for a tool name.
func rpgToolIcon(name string) string {
	switch name {
	case "Read", "ReadFile":
		return "◆"
	case "Write", "WriteFile":
		return "◇"
	case "Edit", "EditFile":
		return "◈"
	case "Bash", "Shell":
		return "▶"
	case "Grep", "Glob":
		return "◉"
	case "WebFetch", "WebSearch":
		return "◎"
	case "Task":
		return "◈"
	default:
		return "○"
	}
}
