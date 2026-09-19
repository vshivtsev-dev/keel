package ui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// theme — все цвета и рамки в одном месте.
//
// Палитра нарочно скупая. Экран keel читают в двух случаях: когда
// восстанавливают хост и когда что-то сломалось, — и в обоих цвет должен
// нести смысл, а не украшать. Четыре значения состояния, и ни одного
// оттенка сверх.
type theme struct {
	// Значения состояния строки. Различать их обязательно: «изменится»,
	// «уже в порядке» и «не настроено в манифесте» — три разные вещи, и
	// путать их значит скрывать от человека, что он забыл ключ.
	change lipgloss.Style
	ok     lipgloss.Style
	skip   lipgloss.Style
	bad    lipgloss.Style
	warn   lipgloss.Style

	title   lipgloss.Style
	dim     lipgloss.Style
	sel     lipgloss.Style
	border  lipgloss.Style
	focused lipgloss.Style
	header  lipgloss.Style
	footer  lipgloss.Style
}

func newTheme() theme {
	// NO_COLOR — общепринятый способ сказать «не надо», и его надо
	// слушать: в консоли Proxmox палитра часто врёт, и подсветка там
	// мешает больше, чем помогает.
	if os.Getenv("NO_COLOR") != "" || os.Getenv("KEEL_NO_COLOR") != "" {
		lipgloss.SetColorProfile(0) // Ascii
	}

	var (
		blue   = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
		green  = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
		yellow = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
		red    = lipgloss.AdaptiveColor{Light: "124", Dark: "203"}
		grey   = lipgloss.AdaptiveColor{Light: "245", Dark: "241"}
		text   = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	)

	base := lipgloss.NewStyle()
	return theme{
		change:  base.Foreground(blue),
		ok:      base.Foreground(green),
		skip:    base.Foreground(grey),
		bad:     base.Foreground(red),
		warn:    base.Foreground(yellow),
		title:   base.Bold(true).Foreground(text),
		dim:     base.Foreground(grey),
		sel:     base.Bold(true),
		border:  base.Border(lipgloss.RoundedBorder()).BorderForeground(grey),
		focused: base.Border(lipgloss.RoundedBorder()).BorderForeground(blue),
		header:  base.Bold(true).Foreground(text),
		footer:  base.Foreground(grey),
	}
}

// pane рисует зону с заголовком в рамке. Рамка подсвечивается, когда зона
// в фокусе: без этого непонятно, куда поедут стрелки.
func (t theme) pane(title, body string, width, height int, focused bool) string {
	style := t.border
	if focused {
		style = t.focused
	}
	head := t.header.Render(truncate(title, width))
	return style.Width(width).MaxWidth(width + 2).Height(height).Render(head + "\n" + body)
}
