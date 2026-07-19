package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/polera/tokeneyes/pkg/tokeneyes"
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[38;5;117m"
	ansiBlue  = "\x1b[38;5;33m"
	ansiWhite = "\x1b[38;5;255m"
	ansiWarn  = "\x1b[38;5;214m"
	ansiPanel = "\x1b[48;5;24m"
)

// printTUI renders a compact, non-interactive terminal dashboard. Keeping the
// renderer non-interactive makes it useful in terminals, captured demos, and
// CI logs without changing the analysis or persistence lifecycle.
func printTUI(w io.Writer, run tokeneyes.Run, saved bool) {
	width := terminalWidth()
	color := tuiColorEnabled()
	r := tuiRenderer{w: w, width: width, color: color}
	r.render(run, saved)
}

type tuiRenderer struct {
	w     io.Writer
	width int
	color bool
}

func (r tuiRenderer) render(run tokeneyes.Run, saved bool) {
	inner := r.width - 4
	sourceLabel := fmt.Sprintf("Scanning %s", plural(len(run.Sources), "source"))
	if run.Incomplete {
		sourceLabel += " (incomplete)"
	}
	summary := r.joinLine("  "+sourceLabel, humanBytes(totalBytes(run.Sources))+"  ")
	fmt.Fprintln(r.w, r.style(ansiPanel+ansiBold+ansiWhite, summary))
	fmt.Fprintln(r.w)

	for i, result := range orderedResults(run) {
		if i > 0 {
			r.rule("─")
		}
		r.model(result, inner)
	}

	r.warnings(run, saved)
	r.rule("─")
	footer := "✓ Estimated locally · source content not saved"
	if run.Config.Verified {
		footer = "✓ Official counting requested · source content not saved"
	}
	if saved {
		footer += " · metadata saved"
	}
	fmt.Fprintln(r.w, "  "+r.style(ansiDim, footer))
}

func orderedResults(run tokeneyes.Run) []tokeneyes.ModelResult {
	results := make([]tokeneyes.ModelResult, 0, len(run.Results))
	used := make([]bool, len(run.Results))
	for _, model := range run.Config.Models {
		for i, result := range run.Results {
			if !used[i] && result.Model == model {
				results = append(results, result)
				used[i] = true
				break
			}
		}
	}
	for i, result := range run.Results {
		if !used[i] {
			results = append(results, result)
		}
	}
	return results
}

func (r tuiRenderer) model(result tokeneyes.ModelResult, inner int) {
	statusColor := ansiCyan
	if result.CapabilityStatus != "supported" {
		statusColor = ansiWarn
	}
	name := "  " + r.style(statusColor, "●") + "  " + r.style(ansiBold+ansiWhite, result.Model)
	tokens := r.style(ansiBold+ansiWhite, commaInt(result.InputTokens)+" tok") + "  "
	r.line(name, tokens)

	barWidth := inner
	if barWidth < 12 {
		barWidth = 12
	}
	filled := int(result.ContextUtilization * float64(barWidth))
	if result.InputTokens > 0 && filled == 0 {
		filled = 1
	}
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("━", filled) + strings.Repeat("─", barWidth-filled)
	fmt.Fprintln(r.w, "  "+r.style(ansiCyan, bar[:]))

	context := fmt.Sprintf("%.2f%% context", result.ContextUtilization*100)
	cost := expectedScenario(result)
	costLabel := "cost unavailable"
	if cost != nil {
		costLabel = "$" + dashboardUSD(cost.CostMicrosUSD) + " expected"
	}
	r.line("  "+r.style(ansiDim, context), r.style(ansiDim, costLabel)+"  ")

	if result.CapabilityStatus != "supported" {
		fmt.Fprintln(r.w, "  "+r.style(ansiWarn, "! "+result.CapabilityStatus))
	}
}

func (r tuiRenderer) warnings(run tokeneyes.Run, saved bool) {
	count := len(uniqueWarnings(run))
	if count == 0 {
		return
	}
	fmt.Fprintln(r.w, "  "+r.style(ansiWarn, "! ")+plural(count, "warning"))
	if saved {
		fmt.Fprintln(r.w, "  "+r.style(ansiDim, "View all warnings in the history view:"))
		fmt.Fprintln(r.w, r.style(ansiDim, "tokeneyes history "+run.ID))
		return
	}
	fmt.Fprintln(r.w, "  "+r.style(ansiDim, "Save the run to view all warnings"))
	fmt.Fprintln(r.w, "  "+r.style(ansiDim, "in the history view."))
}

func (r tuiRenderer) line(left, right string) {
	fmt.Fprintln(r.w, r.joinLine(left, right))
}

func (r tuiRenderer) joinLine(left, right string) string {
	padding := r.width - visibleWidth(left) - visibleWidth(right)
	if padding < 1 {
		padding = 1
	}
	return left + strings.Repeat(" ", padding) + right
}

func (r tuiRenderer) rule(mark string) {
	fmt.Fprintln(r.w, r.style(ansiDim+ansiBlue, strings.Repeat(mark, r.width)))
}

func (r tuiRenderer) style(code, value string) string {
	if !r.color {
		return value
	}
	return code + value + ansiReset
}

func terminalWidth() int {
	width := 80
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 {
		width = value
	}
	if width < 44 {
		width = 44
	}
	if width > 120 {
		width = 120
	}
	return width
}

func tuiColorEnabled() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func expectedScenario(result tokeneyes.ModelResult) *tokeneyes.OutputScenario {
	for i := range result.Scenarios {
		if result.Scenarios[i].Name == "expected" {
			return &result.Scenarios[i]
		}
	}
	if len(result.Scenarios) == 0 {
		return nil
	}
	return &result.Scenarios[len(result.Scenarios)/2]
}

func uniqueWarnings(run tokeneyes.Run) []string {
	seen := make(map[string]bool)
	var warnings []string
	add := func(warning string) {
		if warning != "" && !seen[warning] {
			seen[warning] = true
			warnings = append(warnings, warning)
		}
	}
	for _, warning := range run.Warnings {
		add(warning)
	}
	for _, result := range run.Results {
		for _, warning := range result.Warnings {
			add(warning)
		}
	}
	return warnings
}

func warningNotice(run tokeneyes.Run, saved bool) string {
	count := len(uniqueWarnings(run))
	if count == 0 {
		return ""
	}
	notice := plural(count, "warning")
	if saved {
		return notice + ". View all warnings in the history view: tokeneyes history " + run.ID
	}
	return notice + ". Save the run to view all warnings in the history view."
}

func visibleWidth(value string) int {
	insideEscape := false
	width := 0
	for _, char := range value {
		switch {
		case char == '\x1b':
			insideEscape = true
		case insideEscape && char == 'm':
			insideEscape = false
		case !insideEscape:
			width++
		}
	}
	return width
}

func plural(n int, noun string) string {
	suffix := "s"
	if n == 1 {
		suffix = ""
	}
	return fmt.Sprintf("%d %s%s", n, noun, suffix)
}

func humanBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(n)
	for _, unit := range units {
		value /= 1000
		if value < 1000 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", n)
}

func commaInt(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}

func dashboardUSD(micros int64) string {
	if micros >= 1000 {
		return fmt.Sprintf("%.3f", float64(micros)/1_000_000)
	}
	value := tokeneyes.FormatUSD(micros)
	value = strings.TrimRight(value, "0")
	value = strings.TrimRight(value, ".")
	if value == "" {
		return "0"
	}
	return value
}
