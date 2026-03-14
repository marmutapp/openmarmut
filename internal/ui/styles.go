package ui

import (
	"fmt"
	"strings"
	"time"

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

// FormatSummary returns a summary line like "[3 tool calls | 100 + 50 = 150 tokens | ~$0.01 | 2.3s]".
func FormatSummary(toolCalls, promptTok, completionTok int, cost string, duration time.Duration) string {
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
