package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Color palette.
const (
	ColorBrand   = lipgloss.Color("#00BCD4") // Cyan/teal — primary brand.
	ColorSecond  = lipgloss.Color("#2196F3") // Blue — secondary accent.
	ColorSuccess = lipgloss.Color("#4CAF50") // Green.
	ColorError   = lipgloss.Color("#F44336") // Red.
	ColorWarning = lipgloss.Color("#FFC107") // Yellow/amber.
	ColorDim     = lipgloss.Color("#9E9E9E") // Gray — muted text.
)

// Named styles — all exported so CLI code imports from here.
var (
	BrandStyle      = lipgloss.NewStyle().Bold(true).Foreground(ColorBrand)
	HeaderStyle     = lipgloss.NewStyle().Bold(true).Foreground(ColorBrand)
	ErrorStyle      = lipgloss.NewStyle().Bold(true).Foreground(ColorError)
	SuccessStyle    = lipgloss.NewStyle().Foreground(ColorSuccess)
	WarningStyle    = lipgloss.NewStyle().Foreground(ColorWarning)
	DimStyle        = lipgloss.NewStyle().Foreground(ColorDim)
	ToolCallStyle   = lipgloss.NewStyle().Foreground(ColorBrand).Faint(true)
	ToolNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(ColorBrand)
	ToolArgStyle    = lipgloss.NewStyle().Foreground(ColorDim)
	UserPromptStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBrand)
	CostStyle       = lipgloss.NewStyle().Foreground(ColorDim)
	SeparatorStyle  = lipgloss.NewStyle().Foreground(ColorDim)
	TableHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBrand).Underline(true)

	ConfirmBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorWarning).
			Padding(0, 1)

	BorderBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBrand).
			Padding(0, 1)
)

// styled returns styled text if color is enabled, plain text otherwise.
func styled(s lipgloss.Style, text string) string {
	if !ColorEnabled() {
		return text
	}
	return s.Render(text)
}

// FormatError returns "✗ Error: msg" in red bold.
func FormatError(msg string) string {
	return styled(ErrorStyle, "✗ Error: "+msg)
}

// FormatSuccess returns "✓ msg" in green.
func FormatSuccess(msg string) string {
	return styled(SuccessStyle, "✓ "+msg)
}

// FormatWarning returns "⚠ msg" in yellow.
func FormatWarning(msg string) string {
	return styled(WarningStyle, "⚠ "+msg)
}

// FormatToolCall returns "  → name(args)" in dim cyan.
func FormatToolCall(name, args string) string {
	if !ColorEnabled() {
		return fmt.Sprintf("  → %s(%s)", name, args)
	}
	return fmt.Sprintf("  %s %s%s%s",
		ToolCallStyle.Render("→"),
		ToolNameStyle.Render(name),
		ToolCallStyle.Render("("),
		ToolArgStyle.Render(args),
	) + ToolCallStyle.Render(")")
}

// FormatSummary returns a summary line like "[3 tool calls | 100 + 50 = 150 tokens | ~$0.01 | 2.3s | ctx: 14%]".
// contextPct is optional (-1 to omit).
func FormatSummary(toolCalls, promptTok, completionTok int, cost string, duration time.Duration, contextPct ...int) string {
	var parts []string
	if toolCalls > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls", toolCalls))
	}
	parts = append(parts, fmt.Sprintf("%d + %d = %d tokens",
		promptTok, completionTok, promptTok+completionTok))
	if cost != "" {
		parts = append(parts, "~"+cost)
	}
	parts = append(parts, fmt.Sprintf("%.1fs", duration.Seconds()))

	if len(contextPct) > 0 && contextPct[0] >= 0 {
		parts = append(parts, FormatContextPercent(contextPct[0]))
	}

	line := "[" + strings.Join(parts, " │ ") + "]"
	return styled(DimStyle, line)
}

// FormatKeyValue returns "key: value" with the key dimmed.
func FormatKeyValue(key, value string) string {
	if !ColorEnabled() {
		return key + ": " + value
	}
	return DimStyle.Render(key+":") + " " + value
}

// RenderBox renders content inside a bordered box with a title.
func RenderBox(title, content string) string {
	if !ColorEnabled() {
		return fmt.Sprintf("── %s ──\n%s\n────────", title, content)
	}
	box := BorderBoxStyle.Render(content)
	titleLine := HeaderStyle.Render(title)
	return titleLine + "\n" + box
}

// RenderTable renders a simple table with headers and rows.
// activeRow (0-based) is highlighted; use -1 for no highlight.
func RenderTable(headers []string, rows [][]string, activeRow int) string {
	if len(headers) == 0 {
		return ""
	}

	// Calculate column widths.
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var sb strings.Builder

	// Header row.
	for i, h := range headers {
		if i > 0 {
			sb.WriteString("  ")
		}
		padded := h + strings.Repeat(" ", widths[i]-len(h))
		if ColorEnabled() {
			sb.WriteString(TableHeaderStyle.Render(padded))
		} else {
			sb.WriteString(padded)
		}
	}
	sb.WriteString("\n")

	// Data rows.
	for ri, row := range rows {
		for i := range headers {
			if i > 0 {
				sb.WriteString("  ")
			}
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			padded := cell + strings.Repeat(" ", widths[i]-len(cell))
			if ri == activeRow && ColorEnabled() {
				sb.WriteString(BrandStyle.Render(padded))
			} else {
				sb.WriteString(padded)
			}
		}
		if ri < len(rows)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// RenderWelcomeBanner renders the branded welcome box with session info.
func RenderWelcomeBanner(providerName, model, targetDir, mode string) string {
	lines := []string{
		FormatKeyValue("Provider", providerName+" ("+model+")"),
		FormatKeyValue("Target", targetDir),
		FormatKeyValue("Mode", mode),
		"",
		"Type /help for commands, /quit to exit",
	}
	content := strings.Join(lines, "\n")

	if !ColorEnabled() {
		return fmt.Sprintf("── OpenMarmut ──\n%s\n────────", content)
	}

	title := " " + BrandStyle.Render("OpenMarmut") + " "
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBrand).
		BorderTop(true).
		Padding(0, 1).
		Render(content)
	// Replace the top border segment with the title.
	topBorder := "╭─"
	idx := strings.Index(box, topBorder)
	if idx >= 0 {
		box = box[:idx] + "╭─" + title + strings.Repeat("─", 40) + box[idx+len(topBorder)+42:]
	}
	return box
}

// RenderConfirmBox renders a permission confirmation prompt inside a yellow-bordered box.
// The footer contains the key hint.
func RenderConfirmBox(preview string) string {
	if !ColorEnabled() {
		return fmt.Sprintf("── Permission Required ──\n%s\n── [y]es / [n]o / [a]lways ──", preview)
	}
	box := ConfirmBoxStyle.Render(preview)
	header := WarningStyle.Render("Permission Required")
	footer := DimStyle.Render("[y]es / [n]o / [a]lways")
	return "\n" + header + "\n" + box + "\n" + footer + " "
}

// RenderMarkdown renders markdown text with glamour (dark theme).
// Falls back to plain text if glamour fails or color is disabled.
func RenderMarkdown(md string) string {
	if !ColorEnabled() {
		return md
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.TrimRight(out, "\n")
}

// FormatHint returns a dim hint line like "  Hint: msg".
func FormatHint(msg string) string {
	return styled(DimStyle, "  Hint: "+msg)
}

// FormatProviderType returns the provider type name color-coded by category.
func FormatProviderType(typeName string) string {
	if !ColorEnabled() {
		return typeName
	}
	switch typeName {
	case "openai":
		return SuccessStyle.Render(typeName)
	case "anthropic":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#E040FB")).Render(typeName)
	case "openai-responses":
		return lipgloss.NewStyle().Foreground(ColorSecond).Render(typeName)
	case "gemini":
		return WarningStyle.Render(typeName)
	case "ollama":
		return lipgloss.NewStyle().Foreground(ColorBrand).Render(typeName)
	default:
		return DimStyle.Render(typeName)
	}
}

// FormatPermission colorizes a file permission string character by character.
// r=green, w=yellow, x=red, -=gray.
func FormatPermission(perm string) string {
	if !ColorEnabled() {
		return perm
	}
	var sb strings.Builder
	for _, ch := range perm {
		switch ch {
		case 'r':
			sb.WriteString(SuccessStyle.Render("r"))
		case 'w':
			sb.WriteString(WarningStyle.Render("w"))
		case 'x':
			sb.WriteString(ErrorStyle.Render("x"))
		case 'd':
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(ColorSecond).Render("d"))
		default:
			sb.WriteString(DimStyle.Render(string(ch)))
		}
	}
	return sb.String()
}

// FormatDirEntry formats a directory entry name. Directories are bold blue with trailing /.
func FormatDirEntry(name string, isDir bool) string {
	if isDir {
		dirName := name + "/"
		if !ColorEnabled() {
			return dirName
		}
		return lipgloss.NewStyle().Bold(true).Foreground(ColorSecond).Render(dirName)
	}
	return name
}

// RenderCodeBlock wraps code in a glamour-rendered markdown code block.
// lang is the language hint (e.g., "go", "python"); empty means no hint.
// Falls back to plain text if glamour is unavailable.
func RenderCodeBlock(code, lang string) string {
	if !ColorEnabled() {
		return code
	}
	md := fmt.Sprintf("```%s\n%s\n```", lang, code)
	return RenderMarkdown(md)
}

// TruncateEnd truncates s to max characters, adding "..." if truncated.
func TruncateEnd(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// FormatContextPercent returns "ctx: N%" with color coding.
// Green: 0-59%, Yellow: 60-79%, Red: 80%+.
func FormatContextPercent(pct int) string {
	text := fmt.Sprintf("ctx: %d%%", pct)
	if !ColorEnabled() {
		return text
	}
	switch {
	case pct >= 80:
		return ErrorStyle.Render(text)
	case pct >= 60:
		return WarningStyle.Render(text)
	default:
		return SuccessStyle.Render(text)
	}
}

// RenderProgressBar returns a text progress bar like "██████░░░░░░░░░░░░░░ 45%".
// Width is the number of bar characters. Color coded: green <60%, yellow 60-79%, red 80%+.
func RenderProgressBar(pct, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	filled := (pct * width) / 100
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	label := fmt.Sprintf(" %d%%", pct)

	if !ColorEnabled() {
		return bar + label
	}

	var styledBar string
	switch {
	case pct >= 80:
		styledBar = ErrorStyle.Render(bar)
	case pct >= 60:
		styledBar = WarningStyle.Render(bar)
	default:
		styledBar = SuccessStyle.Render(bar)
	}
	return styledBar + DimStyle.Render(label)
}

// HumanizeBytes formats a byte count into a human-readable string.
func HumanizeBytes(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
