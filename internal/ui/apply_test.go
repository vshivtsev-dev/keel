package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// press прогоняет нажатие через модель, как это делает Bubble Tea.
func press(t *testing.T, m *Model, r rune) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return cmd
}

func pressKey(t *testing.T, m *Model, k tea.KeyType) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(tea.KeyMsg{Type: k})
	return cmd
}

// Маршрут задачи: выбор → подтверждение → ход → итог. Зоны при этом не
// переставляются, меняется их содержимое.
func TestRouteFromChoiceToResult(t *testing.T) {
	m := testModel(t, 120, 30)

	if m.stage != stageChoosing {
		t.Fatalf("начальный шаг маршрута: %v", m.stage)
	}
	press(t, m, 'a')
	if m.stage != stageConfirm {
		t.Fatalf("после «применить» ожидалось подтверждение, получено %v", m.stage)
	}
	if !strings.Contains(m.View(), "Применить") {
		t.Errorf("на экране подтверждения не спрашивают:\n%s", m.View())
	}

	// Передумать можно.
	press(t, m, 'n')
	if m.stage != stageChoosing {
		t.Fatalf("отказ не вернул к выбору: %v", m.stage)
	}
}

// Применять нечего — и сказать об этом надо прямо, а не открывать пустое
// подтверждение.
func TestRefusesToApplyNothing(t *testing.T) {
	m := testModel(t, 120, 30)
	m.chosen = map[string]bool{}

	press(t, m, 'a')
	if m.stage == stageConfirm {
		t.Error("открылось подтверждение пустого плана")
	}
	if !strings.Contains(m.status, "не отмечена") {
		t.Errorf("не сказано, почему ничего не происходит: %q", m.status)
	}
}

func TestSpaceTogglesSelection(t *testing.T) {
	m := testModel(t, 120, 30)
	r := m.current()
	if r == nil || r.kind != rowProvider {
		t.Fatalf("курсор не на области: %+v", r)
	}
	was := m.chosen[r.id]
	pressKey(t, m, tea.KeySpace)
	if m.chosen[r.id] == was {
		t.Error("пробел не переключил отметку")
	}
}

// Отмечать можно только то, что и правда меняется: «уже в порядке» и
// «не настроено» отмечать бессмысленно.
func TestOnlyChangingProvidersCanBeChosen(t *testing.T) {
	m := testModel(t, 120, 30)
	for i, r := range m.rows {
		if r.kind == rowProvider && r.status != engine.StatusChanges {
			m.cursor = i
			pressKey(t, m, tea.KeySpace)
			if m.chosen[r.id] {
				t.Errorf("отмечена область без изменений: %s (%s)", r.id, r.status)
			}
		}
	}
}

func TestEnterExpandsProvider(t *testing.T) {
	m := testModel(t, 120, 30)
	r := m.current()
	before := len(m.rows)

	pressKey(t, m, tea.KeyEnter)
	if len(m.rows) <= before {
		t.Errorf("область не развернулась: было %d строк, стало %d", before, len(m.rows))
	}
	if !strings.Contains(m.View(), "установить обновления") {
		t.Errorf("шаги не показаны:\n%s", m.View())
	}
	// Курсор должен остаться на той же области, а не уехать.
	if got := m.current(); got == nil || got.id != r.id {
		t.Errorf("курсор уехал: %+v", got)
	}
}

// Выбранное должно быть видно в сводке: это и есть зона «что получится».
func TestSummaryShowsSelection(t *testing.T) {
	m := testModel(t, 120, 30)
	view := m.View()
	if !strings.Contains(view, "2 шага в 2 областях") {
		t.Errorf("сводка не считает выбранное:\n%s", view)
	}

	m.chosen = map[string]bool{"host/storage": true}
	if !strings.Contains(m.View(), "1 шаг в 1 области") {
		t.Errorf("сводка не обновилась после смены выбора:\n%s", m.View())
	}
}

// Шаг, ждавший упавшего, выполнять нельзя.
func TestStepsWaitingOnFailureAreSkipped(t *testing.T) {
	m := newTestModel(t, 120, 30, false)
	m.plan = &plan.Plan{Version: plan.FormatVersion, Steps: []plan.Step{
		{ID: "первый", Provider: "host/storage", Summary: "упадёт",
			Action: plan.ActionExec, Cmd: []string{"false"}},
		{ID: "второй", Provider: "host/storage", Summary: "зависит от первого",
			Action: plan.ActionExec, Cmd: []string{"true"}, Needs: []string{"первый"}},
	}}
	m.chosen = map[string]bool{"host/storage": true}

	cmd := m.apply()
	drain(t, m, cmd)

	if len(m.report.Failed) != 1 {
		t.Fatalf("сбой не зафиксирован: %+v", m.report)
	}
	if len(m.report.Skipped) != 1 || m.report.Skipped[0].ID != "второй" {
		t.Fatalf("зависимый шаг не пропущен: %+v", m.report.Skipped)
	}
}

func TestApplyRunsStepsAndReports(t *testing.T) {
	m := newTestModel(t, 120, 30, false)
	m.plan = &plan.Plan{Version: plan.FormatVersion, Steps: []plan.Step{
		{ID: "s1", Provider: "host/storage", Summary: "поздороваться",
			Action: plan.ActionExec, Cmd: []string{"echo", "привет"}},
	}}
	m.chosen = map[string]bool{"host/storage": true}

	drain(t, m, m.apply())

	if len(m.report.Done) != 1 {
		t.Fatalf("шаг не выполнен: %+v", m.report)
	}
	if m.stage != stageDone {
		t.Fatalf("маршрут не дошёл до итога: %v", m.stage)
	}
	view := m.View()
	if !strings.Contains(view, "выполнено") {
		t.Errorf("итог не показан:\n%s", view)
	}
	// Вывод команды должен попасть на экран: молчащий экран на долгой
	// команде выглядит как зависание.
	if !strings.Contains(strings.Join(m.progress, "\n"), "привет") {
		t.Errorf("вывод команды потерян: %v", m.progress)
	}
}

// drain крутит сообщения, пока применение не закончится, — тем же
// способом, каким это делает Bubble Tea: команда может вернуть пакет
// других команд, и их тоже надо выполнить.
func drain(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	m.stage = stageRunning

	queue := []tea.Cmd{cmd}
	for i := 0; len(queue) > 0 && i < 200; i++ {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		msg := next()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		// Тик сам себя перезапускает — в тесте это был бы вечный цикл.
		if _, ok := msg.(tickMsg); ok {
			continue
		}
		model, produced := m.Update(msg)
		m = model.(*Model)
		queue = append(queue, produced)
		if m.stage == stageDone {
			return
		}
	}
}

// Клавиш больше, чем влезает в строку подсказок, и половина из них нужна
// раз в жизни — значит должен быть экран, где они все.
func TestHelpScreen(t *testing.T) {
	m := testModel(t, 120, 30)

	press(t, m, '?')
	view := m.View()
	for _, want := range []string{"Клавиши", "применить", "пересчитать", "русской раскладке", "Значки"} {
		if !strings.Contains(view, want) {
			t.Errorf("в справке нет %q:\n%s", want, view)
		}
	}

	press(t, m, 'q')
	if m.help {
		t.Error("справка не закрылась")
	}
	// И выход не сработал вместо закрытия справки.
	if m.stage != stageChoosing {
		t.Errorf("после закрытия справки маршрут сбился: %v", m.stage)
	}
}

// Справка открывается и по-русски.
func TestHelpFromCyrillicLayout(t *testing.T) {
	m := testModel(t, 120, 30)
	press(t, m, '?')
	press(t, m, 'й')
	if m.help {
		t.Error("справка не закрылась по «й»")
	}
}
