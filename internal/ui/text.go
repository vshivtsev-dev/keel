package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// truncate обрезает строку по видимой ширине, не считая управляющих
// последовательностей и учитывая, что кириллица шире ASCII не бывает, а
// вот значки бывают.
func truncate(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	// Режем по рунам и меряем видимую ширину: наивный срез по байтам
	// разрубил бы и букву, и цветовую последовательность.
	var b strings.Builder
	inEscape := false
	visible := 0
	for _, r := range s {
		if r == '\033' {
			inEscape = true
		}
		if inEscape {
			b.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		w := lipgloss.Width(string(r))
		if visible+w > width-1 {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		visible += w
	}
	return b.String() + "\033[0m"
}

// wrap переносит текст по ширине, не разрывая слова.
func wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if lipgloss.Width(para) <= width {
			out = append(out, para)
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
