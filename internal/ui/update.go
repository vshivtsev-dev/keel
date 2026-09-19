package ui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vshivtsev-dev/keel/internal/cli"
	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := key(msg)

	// Выйти можно всегда и откуда угодно — кроме середины применения:
	// бросить хост на полпути хуже, чем дождаться.
	// Помощь открывается и закрывается откуда угодно.
	if k == "?" || (m.help && (k == "esc" || k == "q")) {
		m.help = !m.help
		return m, nil
	}
	if m.help {
		return m, nil
	}

	switch k {
	case "ctrl+c":
		if m.stage == stageRunning {
			m.status = "идёт применение — прервать можно только подтверждением шага"
			return m, nil
		}
		return m, tea.Quit
	case "q", "esc":
		if m.stage == stageRunning {
			return m, nil
		}
		if m.showAll {
			m.showAll = false
			return m, nil
		}
		return m, tea.Quit
	}

	switch m.stage {
	case stageChoosing:
		return m.onChoosingKey(k)
	case stageConfirm:
		return m.onConfirmKey(k)
	case stageDone, stageError:
		if k == "p" {
			return m.restart()
		}
	}
	return m, nil
}

func (m *Model) onChoosingKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.rows) - 1
	case "tab":
		m.focus = 1 - m.focus

	case " ":
		if r := m.current(); r != nil && r.kind == rowProvider && r.status == engine.StatusChanges {
			m.chosen[r.id] = !m.chosen[r.id]
		}
	case "enter", "d":
		// Enter разворачивает область: шаги видно сразу, и не приходится
		// гадать, что скрывается за «→ Хранилища».
		if r := m.current(); r != nil && r.id != "" {
			m.expanded[r.id] = !m.expanded[r.id]
			m.rebuild()
		}
	case "f":
		m.showAll = !m.showAll

	case "A":
		for _, r := range m.results {
			if r.Status == engine.StatusChanges {
				m.chosen[r.Provider] = true
			}
		}
	case "N":
		m.chosen = map[string]bool{}

	case "p":
		return m.restart()

	case "a":
		if m.selectedSteps() == 0 {
			m.status = "нечего применять: ни одна область не отмечена"
			return m, nil
		}
		m.stage = stageConfirm
	}
	return m, nil
}

func (m *Model) onConfirmKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "enter", "y", "a":
		m.stage = stageRunning
		m.progress = nil
		return m, m.apply()
	case "n", "esc", "q":
		m.stage = stageChoosing
	}
	return m, nil
}

func (m *Model) restart() (tea.Model, tea.Cmd) {
	m.stage = stageLoading
	m.status = "пересчитываю…"
	m.err = nil
	return m, m.collect()
}

func (m *Model) moveCursor(delta int) {
	for i := m.cursor + delta; i >= 0 && i < len(m.rows); i += delta {
		// Заголовки разделов курсор проскакивает: делать с ними нечего.
		if m.rows[i].kind != rowGroup {
			m.cursor = i
			return
		}
	}
}

func (m *Model) current() *row {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

func (m *Model) rebuild() {
	id := ""
	if r := m.current(); r != nil {
		id = r.id
	}
	m.rows = buildRows(m.results, m.expanded)
	// После сворачивания курсор должен остаться на той же области, а не
	// уехать на случайную строку.
	for i, r := range m.rows {
		if r.kind == rowProvider && r.id == id {
			m.cursor = i
			return
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

// selectedPlan — план из отмеченных областей.
//
// Пустой выбор здесь значит «ничего», а не «всё». В командной строке
// наоборот: `keel apply` без уточнений применяет план целиком. Разница
// намеренная — на экране отметки видны, и снять их все можно только
// нарочно, а в команде их просто нет.
func (m *Model) selectedPlan() *plan.Plan {
	if m.plan == nil {
		return &plan.Plan{}
	}
	ids := map[string]bool{}
	for id, on := range m.chosen {
		if on {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return &plan.Plan{Version: m.plan.Version}
	}
	return m.plan.Select(ids)
}

func (m *Model) selectedSteps() int { return len(m.selectedPlan().Steps) }

// --- применение по шагам ------------------------------------------------------
//
// План исполняется не одним куском, а шаг за шагом через цикл сообщений.
// Иначе не получится двух вещей сразу: показывать вывод, пока команда
// работает, и отдавать терминал целиком тому шагу, который может
// спросить. apt делает и то и другое — сначала молчит минутами, потом
// спрашивает про изменённый конфиг.

type runner struct {
	plan   *plan.Plan
	index  int
	exec   *exec.Runner
	sink   *teaWriter
	report engine.ApplyReport
	failed map[string]bool
}

// apply готовит исполнение и запускает первый шаг.
func (m *Model) apply() tea.Cmd {
	sink := &teaWriter{}
	m.run = &runner{
		plan: m.selectedPlan(),
		sink: sink,
		exec: &exec.Runner{
			Out: sink, Log: m.app.Log, Mask: m.app.Masker.Apply,
			Resolve:   m.app.Secrets.Resolve,
			BackupDir: m.app.Paths.Backups(),
			Stamp:     time.Now().Format("2006-01-02_150405"),
			Sys:       m.app.Paths.Sys,
			Sandboxed: m.app.Paths.Sandboxed(),
			Guards:    cli.Guards(m.facts),
		},
		failed: map[string]bool{},
	}
	return tea.Batch(m.nextStep(), tickCmd())
}

type stepDoneMsg struct {
	step plan.Step
	err  error
}

type finishedMsg struct{}

type tickMsg struct{}

// tickCmd подтягивает вывод работающей команды на экран. Без него панель
// «ход дела» оживала бы только после завершения шага, а молчащий экран
// на dist-upgrade выглядит как зависание.
func tickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) nextStep() tea.Cmd {
	r := m.run
	if r == nil || r.index >= len(r.plan.Steps) {
		return func() tea.Msg { return finishedMsg{} }
	}
	step := r.plan.Steps[r.index]
	r.index++

	// Шаг, который ждал упавшего, выполнять нельзя: поставить
	// обновления, список которых не обновился, — не то же самое, что
	// не ставить их вовсе.
	for _, need := range step.Needs {
		if r.failed[need] {
			r.report.Skipped = append(r.report.Skipped, step)
			r.failed[step.ID] = true
			return m.nextStep()
		}
	}

	// Интерактивному шагу отдаём терминал целиком.
	if step.Interactive && step.Action == plan.ActionExec {
		cmd, err := r.exec.Prepare(m.ctx, step)
		if err != nil {
			return func() tea.Msg { return stepDoneMsg{step: step, err: err} }
		}
		if cmd == nil {
			return m.nextStep()
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return stepDoneMsg{step: step, err: r.exec.Finish(step, err)}
		})
	}

	return func() tea.Msg {
		return stepDoneMsg{step: step, err: r.exec.Do(m.ctx, step)}
	}
}

// onStepDone раскладывает исход шага по отчёту и берётся за следующий.
func (m *Model) onStepDone(msg stepDoneMsg) tea.Cmd {
	r := m.run
	var guarded *exec.ErrGuarded
	switch {
	case msg.err == nil:
		r.report.Done = append(r.report.Done, msg.step)
	case errors.Is(msg.err, exec.ErrAborted):
		r.report.Aborted = true
		return func() tea.Msg { return finishedMsg{} }
	case errors.As(msg.err, &guarded):
		r.report.Guarded = append(r.report.Guarded, engine.StepError{Step: msg.step, Err: msg.err})
		r.failed[msg.step.ID] = true
	default:
		r.report.Failed = append(r.report.Failed, engine.StepError{Step: msg.step, Err: msg.err})
		r.failed[msg.step.ID] = true
	}
	m.progress = r.sink.snapshot()
	return m.nextStep()
}

// afterApply делает то, что положено после применения: записывает, чем
// keel тронул видеокарту. Без этой записи откат невозможен, а откат
// здесь — вопрос того, увидит ли человек снова картинку на мониторе.
func (m *Model) afterApply() tea.Cmd {
	if m.app.Paths.Sandboxed() {
		return nil
	}
	return func() tea.Msg {
		if err := m.app.RecordGPUState(m.manifest, m.facts, m.run.report, m.run.exec.Stamp); err != nil {
			return noteMsg(err.Error())
		}
		return nil
	}
}

type noteMsg string
