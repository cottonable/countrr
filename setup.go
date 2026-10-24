package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Palette ────────────────────────────────────────────────────────
var (
	purple = lipgloss.Color("#7D56F4") // primary brand
	pink   = lipgloss.Color("#FF5F87") // accent / selection / active
	cyan   = lipgloss.Color("#04D9FF") // info highlights / tags
	bg     = lipgloss.Color("#171717") // deep charcoal background
	fg     = lipgloss.Color("#e4e4e7") // light text on bg
	muted  = lipgloss.Color("#71717a") // secondary text

	titleStyle  = lipgloss.NewStyle().Foreground(purple).Bold(true)
	labelStyle  = lipgloss.NewStyle().Foreground(fg).Bold(false)
	valueStyle  = lipgloss.NewStyle().Foreground(fg)
	hintStyle   = lipgloss.NewStyle().Foreground(muted).Italic(true)
	tagStyle    = lipgloss.NewStyle().Foreground(cyan).Background(bg).Padding(0, 1)
	selectedBg  = lipgloss.NewStyle().Foreground(fg).Background(pink).Bold(true)
	doneStyle   = lipgloss.NewStyle().Foreground(pink).Bold(true)
	border      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(purple).Padding(0, 1)
	footer      = lipgloss.NewStyle().Foreground(muted)
	cursorBlock = lipgloss.NewStyle().Foreground(bg).Background(fg).Bold(true)
	inputStyle  = lipgloss.NewStyle().Foreground(fg)
	// Log styles for main.go
	logTimestamp = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	logInfo      = lipgloss.NewStyle().Foreground(fg)
	logSuccess   = lipgloss.NewStyle().Foreground(lipgloss.Color("#a6e3a1"))
	logWarning   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9e2af"))
	logError     = lipgloss.NewStyle().Foreground(lipgloss.Color("#f38686"))
)

type fieldDef struct {
	name   string
	label  string
	defVal string
	help   string
}

func fieldDefs() []fieldDef {
	dc := DefaultConfig()
	return []fieldDef{
		{name: "port", label: "Server Port", defVal: strconv.Itoa(dc.Port), help: fmt.Sprintf("HTTP listening port (1–65535). Default: %d", dc.Port)},
		{name: "counter_file", label: "Counter File", defVal: dc.CounterFile, help: "Path used for storing visit counts."},
		{name: "backup_count", label: "Counter Backups", defVal: strconv.FormatInt(dc.BackupCount, 10), help: "Timestamped snapshots kept next to the counter file (YYMMDD-HHMMSS). 0 disables. Default: " + fmt.Sprint(dc.BackupCount)},
		{name: "persist_interval_s", label: "Persist Interval (s)", defVal: strconv.FormatInt(dc.PersistIntervalS, 10), help: "Disk-write frequency. Default: " + fmt.Sprint(dc.PersistIntervalS) + "s"},
		{name: "check_interval_s", label: "Check Interval (s)", defVal: strconv.FormatInt(dc.CheckIntervalS, 10), help: "Log-delta interval. Default: " + fmt.Sprint(dc.CheckIntervalS) + "s"},
		{name: "rate_limit_window_s", label: "Rate Limit Window (s)", defVal: strconv.FormatInt(dc.RateLimitWindowS, 10), help: "IP cooldown before allowing another request. Default: " + fmt.Sprint(dc.RateLimitWindowS) + "s"},
		{name: "rate_limit_sweep_s", label: "Rate Limit Sweep (s)", defVal: strconv.FormatInt(dc.RateLimitSweepS, 10), help: "How often expired IP cooldowns are pruned from memory. Default: " + fmt.Sprint(dc.RateLimitSweepS) + "s"},
		{name: "min_increment_interval_ms", label: "Min Increment Interval (ms)", defVal: strconv.FormatInt(dc.MinIncrementIntervalMs, 10), help: "Global minimum spacing between counted visits (1000 = 1s, 500 = 0.5s). 0 disables. Default: " + fmt.Sprint(dc.MinIncrementIntervalMs) + "ms"},
	}
}

func defaultStr(cfg Config, name string) string {
	switch name {
	case "port":
		return strconv.Itoa(cfg.Port)
	case "counter_file":
		return cfg.CounterFile
	case "backup_count":
		return strconv.FormatInt(cfg.BackupCount, 10)
	case "persist_interval_s":
		return strconv.FormatInt(cfg.PersistIntervalS, 10)
	case "check_interval_s":
		return strconv.FormatInt(cfg.CheckIntervalS, 10)
	case "rate_limit_window_s":
		return strconv.FormatInt(cfg.RateLimitWindowS, 10)
	case "rate_limit_sweep_s":
		return strconv.FormatInt(cfg.RateLimitSweepS, 10)
	case "min_increment_interval_ms":
		return strconv.FormatInt(cfg.MinIncrementIntervalMs, 10)
	}
	return ""
}

func displayValue(val, defVal string) string {
	if val == "" && defVal != "" {
		return hintStyle.Render("(default)")
	}
	if val == "" {
		return valueStyle.Render("(empty)")
	}
	return valueStyle.Render(val)
}

// Model is the TUI state machine.
type Model struct {
	fields    []fieldDef
	values    map[string]string
	selected  int
	editing   bool
	input     string
	cursorPos int
	width     int
	height    int
	done      bool
	cancelled bool
	err       error
}

func InitSetup(cfg Config) Config {
	defs := fieldDefs()
	vals := make(map[string]string, len(defs))
	for _, f := range defs {
		v := defaultStr(cfg, f.name)
		if v == "" {
			v = f.defVal
		}
		vals[f.name] = v
	}
	m := &Model{fields: defs, values: vals, selected: 0, width: 80, height: 24}

	if !isTTY() || m.width < 40 {
		return runStdio(m)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("screen mode unavailable, switching to non-interactive mode:")
		fmt.Println("  ", err)
		return runStdioFallback(m)
	}
	if m.cancelled {
		fmt.Println(logWarning.Render("setup cancelled, configuration not saved."))
		return cfg
	}
	writeAndExit(m)
	return m.toConfig(cfg)
}

func isTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	if (info.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	stderrInfo, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (stderrInfo.Mode() & os.ModeCharDevice) != 0
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.err != nil {
			m.err = nil
			return m, nil
		}
		key := msg.String()

		if m.done {
			return m, tea.Quit
		}

		if m.editing {
			switch key {
			case "ctrl+c":
				m.cancelled = true
				return m, tea.Quit
			case "esc", "enter":
				m.editing = false
				m.values[m.fields[m.selected].name] = strings.TrimSpace(m.input)
				m.input = ""
				m.cursorPos = 0
				return m, nil
			case "left":
				if m.cursorPos > 0 {
					m.cursorPos--
				}
				return m, nil
			case "right":
				if m.cursorPos < len(m.input) {
					m.cursorPos++
				}
				return m, nil
			case "home":
				m.cursorPos = 0
				return m, nil
			case "end":
				m.cursorPos = len(m.input)
				return m, nil
			case "backspace", "shift+backspace":
				if m.cursorPos > 0 {
					m.input = m.input[:m.cursorPos-1] + m.input[m.cursorPos:]
					m.cursorPos--
				}
				return m, nil
			case "delete":
				if m.cursorPos < len(m.input) {
					m.input = m.input[:m.cursorPos] + m.input[m.cursorPos+1:]
				}
				return m, nil
			default:
				if len(key) == 1 {
					m.input = m.input[:m.cursorPos] + key + m.input[m.cursorPos:]
					m.cursorPos++
				}
				return m, nil
			}
		}

		switch key {
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "q":
			m.done = true
			return m, tea.Quit
		case "enter":
			m.editing = true
			m.input = m.values[m.fields[m.selected].name]
			m.cursorPos = len(m.input)
			return m, nil
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
			return m, nil
		case "down", "j":
			if m.selected < len(m.fields)-1 {
				m.selected++
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) View() string {
	maxLabel := 0
	for _, f := range m.fields {
		if len(f.label) > maxLabel {
			maxLabel = len(f.label)
		}
	}

	var lines []string

	// ── Header ──
	lines = append(lines, border.Render(titleStyle.Render(" countrr configuration "+strings.Repeat("·", 40))))
	lines = append(lines, "")

	if m.err != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("#f38686")).Render("✗ "+m.err.Error()))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("press enter to continue"))
		fieldRows := m.viewFieldRows(maxLabel)
		bottom := fieldRows + "\n" + footer.Render(m.footerHint())
		content := lipgloss.NewStyle().Width(m.width).Render(bottom)
		return padToHeight(content, m.height)
	}

	if m.done {
		centered := lipgloss.NewStyle().Width(m.width).Align(lipgloss.Center).Render(doneStyle.Render("✓ config.json written successfully.") + "\n")
		bottom := centered + "\n\n" + hintStyle.Render("press any key to exit")
		return padToHeight(bottom, m.height)
	}

	// ── Fields ──
	for i, f := range m.fields {
		isSelected := i == m.selected
		inEdit := isSelected && m.editing

		dispVal := displayValue(m.values[f.name], f.defVal)

		if inEdit {
			row := formatFieldRow(isSelected, f.label, renderInput(m.input, m.cursorPos), maxLabel, selectedBg, labelStyle, valueStyle)
			lines = append(lines, row)
		} else {
			row := formatFieldRow(isSelected, f.label, dispVal, maxLabel, selectedBg, labelStyle, valueStyle)
			lines = append(lines, row)
		}
	}

	// ── Divider ──
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(muted).Render(strings.Repeat("─", m.width)))
	lines = append(lines, "")

	// ── Help tag ──
	f := m.fields[m.selected]
	lines = append(lines, tagStyle.Render("help")+hintStyle.Render("  "+f.help))
	lines = append(lines, "")
	lines = append(lines, footer.Render(m.footerHint()))

	bottom := strings.Join(lines, "\n")

	return wrapLines(bottom, m.width)
}

func (m *Model) viewFieldRows(maxLabel int) string {
	var lines []string
	for i, f := range m.fields {
		isSelected := i == m.selected
		inEdit := isSelected && m.editing
		dispVal := displayValue(m.values[f.name], f.defVal)
		if inEdit {
			row := formatFieldRow(isSelected, f.label, renderInput(m.input, m.cursorPos), maxLabel, selectedBg, labelStyle, valueStyle)
			lines = append(lines, row)
		} else {
			row := formatFieldRow(isSelected, f.label, dispVal, maxLabel, selectedBg, labelStyle, valueStyle)
			lines = append(lines, row)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) footerHint() string {
	if m.editing {
		return " ←→ move  enter/esc accept  ctrl+c cancel "
	}
	return " enter edit  ↑↓ move  q save & finish "
}

// renderInput draws the value being edited with a solid block cursor sitting on
// the character at cursorPos, so it is obvious where the caret is.
func renderInput(value string, cursorPos int) string {
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(value) {
		cursorPos = len(value)
	}
	before := inputStyle.Render(value[:cursorPos])
	if cursorPos >= len(value) {
		return before + cursorBlock.Render(" ")
	}
	onCursor := cursorBlock.Render(string(value[cursorPos]))
	after := inputStyle.Render(value[cursorPos+1:])
	return before + onCursor + after
}

func formatFieldRow(isSelected bool, label, value string, maxLabel int, sel, lab, val lipgloss.Style) string {
	padding := strings.Repeat(" ", maxLabel-len(label)+1)
	if isSelected {
		// The highlight stops at the colon so the value, and the edit cursor,
		// sit on the normal background.
		return sel.Render("▸ "+label+padding+": ") + val.Render(value)
	}
	return "  " + lab.Render(label+padding+": ") + val.Render(value)
}

func padToHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

func wrapLines(s string, w int) string {
	return lipgloss.NewStyle().Width(w).Render(s)
}

func paddingVert(n int, s string) string {
	empty := strings.Repeat("\n", n)
	return empty + s
}

func (m *Model) toConfig(cfg Config) Config {
	cfg = DefaultConfig()
	for _, f := range m.fields {
		v := m.values[f.name]
		switch f.name {
		case "port":
			if v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
					cfg.Port = n
				}
			}
		case "counter_file":
			cfg.CounterFile = v
		case "backup_count":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					cfg.BackupCount = n
				}
			}
		case "persist_interval_s":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
					cfg.PersistIntervalS = n
				}
			}
		case "check_interval_s":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
					cfg.CheckIntervalS = n
				}
			}
		case "rate_limit_window_s":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
					cfg.RateLimitWindowS = n
				}
			}
		case "rate_limit_sweep_s":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
					cfg.RateLimitSweepS = n
				}
			}
		case "min_increment_interval_ms":
			if v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					cfg.MinIncrementIntervalMs = n
				}
			}
		}
	}
	return cfg
}

func runStdio(m *Model) Config {
	for i, f := range m.fields {
		cur := displayValue(m.values[f.name], f.defVal)
		fmt.Printf("\n%d. %s\n   %s\n   %s > ", i+1, f.label, f.help, cur)
		var input string
		fmt.Scanln(&input)
		if input != "" {
			m.values[f.name] = input
		}
	}
	writeAndExit(m)
	return m.toConfig(DefaultConfig())
}

func runStdioFallback(m *Model) Config {
	for i, f := range m.fields {
		cur := m.values[f.name]
		if cur == "" {
			cur = f.defVal
		}
		fmt.Printf("\n%d. %s  [%s] > ", i+1, f.label, cur)
		var input string
		fmt.Scanln(&input)
		if input != "" {
			m.values[f.name] = input
		}
	}
	writeAndExit(m)
	return m.toConfig(DefaultConfig())
}

func writeAndExit(m *Model) {
	if err := WriteConfig(resolvePath("config.json"), m.toConfig(DefaultConfig())); err != nil {
		fmt.Println(logError.Render("failed to write config:", err.Error()))
		os.Exit(1)
	}
	fmt.Println(logSuccess.Render("config.json created successfully."))
}
