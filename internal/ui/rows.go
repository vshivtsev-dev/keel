package ui

import (
	"fmt"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

type rowKind int

const (
	rowGroup rowKind = iota
	rowProvider
	rowStep
	rowNote
)

// row — строка рабочей зоны. Дерево здесь плоское нарочно: так стрелки
// ходят по нему одинаково, а сворачивание и разворачивание меняет лишь
// то, какие строки в него попадают.
type row struct {
	kind  rowKind
	depth int
	// id провайдера, которому строка принадлежит.
	id     string
	title  string
	status engine.Status
	// step заполнен у строк-шагов; по нему рисуется подробность.
	step *plan.Step
	note *plan.Note
}

// group раскладывает провайдеров по двум разделам, как это видит человек:
// хост и гости. Порядок применения при этом не меняется — он свой, и
// живёт в реестре провайдеров.
func groupOf(id string) string {
	if strings.HasPrefix(id, "guests") {
		return "Гости"
	}
	return "Хост"
}

// buildRows собирает дерево из результатов сборки плана.
func buildRows(results []engine.Result, expanded map[string]bool) []row {
	var rows []row
	lastGroup := ""

	for _, r := range results {
		if g := groupOf(r.Provider); g != lastGroup {
			rows = append(rows, row{kind: rowGroup, title: g})
			lastGroup = g
		}
		rows = append(rows, row{
			kind: rowProvider, depth: 1, id: r.Provider,
			title: r.Title, status: r.Status,
		})
		if !expanded[r.Provider] {
			continue
		}
		for i := range r.Steps {
			rows = append(rows, row{kind: rowStep, depth: 2, id: r.Provider,
				title: r.Steps[i].Summary, step: &r.Steps[i]})
		}
		for i := range r.Notes {
			rows = append(rows, row{kind: rowNote, depth: 2, id: r.Provider,
				title: firstLine(r.Notes[i].Message), note: &r.Notes[i]})
		}
	}
	return rows
}

// mark — значок состояния. Три разных значения у трёх разных вещей, и
// путать их нельзя:
//
//	→ изменится
//	✓ уже в нужном состоянии
//	· в манифесте про это не сказано — правило нуля, это норма
//	✗ не удалось разобраться в состоянии
func (t theme) mark(s engine.Status) string {
	switch s {
	case engine.StatusChanges:
		return t.change.Render("→")
	case engine.StatusOK:
		return t.ok.Render("✓")
	case engine.StatusUnconfigured:
		return t.skip.Render("·")
	case engine.StatusFailed:
		return t.bad.Render("✗")
	}
	return " "
}

func (m *Model) renderRow(r row, index int, width int) string {
	cursor := "  "
	if index == m.cursor {
		cursor = m.theme.change.Render("▸ ")
	}

	switch r.kind {
	case rowGroup:
		return "\n" + m.theme.title.Render(r.title)

	case rowProvider:
		box := "   "
		if r.status == engine.StatusChanges {
			box = "[ ]"
			if m.chosen[r.id] {
				box = m.theme.change.Render("[x]")
			}
		}
		line := fmt.Sprintf("%s%s %s %s", cursor, box, m.theme.mark(r.status), r.title)
		if n := m.stepCount(r.id); n > 0 {
			line += m.theme.dim.Render(fmt.Sprintf("  %d", n))
		}
		if r.status == engine.StatusUnconfigured {
			line = fmt.Sprintf("%s%s %s %s", cursor, box, m.theme.mark(r.status),
				m.theme.skip.Render(r.title))
		}
		return clip(line, width)

	case rowStep:
		return clip(cursor+"      "+m.theme.dim.Render(r.title), width)

	case rowNote:
		return clip(cursor+"      "+m.theme.warn.Render("! ")+m.theme.dim.Render(r.title), width)
	}
	return ""
}

func (m *Model) stepCount(id string) int {
	for _, r := range m.results {
		if r.Provider == id {
			return len(r.Steps)
		}
	}
	return 0
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// clip обрезает строку по ширине с учётом того, что в ней есть цветовые
// последовательности, а буквы могут быть шире одной ячейки.
func clip(s string, width int) string {
	if width <= 0 {
		return s
	}
	return truncate(s, width)
}
