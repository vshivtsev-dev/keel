// Package ui — экран keel.
//
// Устроен он вокруг одной мысли: человек должен видеть состояние хоста,
// свой выбор и ход применения одновременно. Диалоги whiptail этого не
// умели, и потому длинное применение превращалось в череду подтверждений,
// которые перестаёшь читать к пятому.
//
// Экран — это маршрут одной задачи: выбор → сводка → подтверждение → ход →
// итог. Зоны при этом не переставляются; меняется их содержимое.
package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vshivtsev-dev/keel/internal/cli"
	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

type stage int

const (
	// stageLoading — факты о хосте собираются. Первый кадр рисуется сразу,
	// не дожидаясь их: pvesm, apt и lspci отвечают секундами, и молчащий
	// экран в это время выглядит как зависание.
	stageLoading stage = iota
	stageChoosing
	stageConfirm
	stageRunning
	stageDone
	stageError
)

type zone int

const (
	zoneTree zone = iota
	zoneDetails
)

type Model struct {
	app   *cli.App
	ctx   context.Context
	theme theme

	width, height int
	stage         stage
	focus         zone

	manifest *manifest.Manifest
	facts    *facts.Facts
	plan     *plan.Plan
	results  []engine.Result

	rows     []row
	cursor   int
	chosen   map[string]bool
	expanded map[string]bool

	// Ход применения.
	run      *runner
	progress []string
	report   engine.ApplyReport

	err     error
	status  string
	showAll bool
	// help — открыт экран помощи. Отдельным экраном, а не подсказкой в
	// углу: клавиш больше, чем влезает в строку, и половина из них
	// нужна раз в жизни.
	help bool
}

func New(ctx context.Context, app *cli.App) *Model {
	return &Model{
		app: app, ctx: ctx, theme: newTheme(),
		stage:    stageLoading,
		chosen:   map[string]bool{},
		expanded: map[string]bool{},
		status:   "собираю состояние хоста…",
		// До первого сообщения о размере окна рисуем по размеру, который
		// влезет куда угодно: пустой экран хуже тесного.
		width: 80, height: 24,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.collect(), tea.EnterAltScreen)
}

// --- сообщения ---------------------------------------------------------------

type collectedMsg struct {
	manifest *manifest.Manifest
	facts    *facts.Facts
	plan     *plan.Plan
	results  []engine.Result
	err      error
}

// collect собирает факты и план в стороне от отрисовки: экран обязан
// оставаться живым, пока хост отвечает.
func (m *Model) collect() tea.Cmd {
	return func() tea.Msg {
		mf, err := m.app.LoadManifest()
		if err != nil {
			return collectedMsg{err: err}
		}
		f := m.app.FactsFor(m.ctx, mf)
		eng, err := m.app.Engine()
		if err != nil {
			return collectedMsg{err: err}
		}
		p, results := eng.Collect(m.ctx, mf, f)
		return collectedMsg{manifest: mf, facts: f, plan: p, results: results}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case collectedMsg:
		if msg.err != nil {
			m.stage, m.err = stageError, msg.err
			return m, nil
		}
		m.manifest, m.facts, m.plan, m.results = msg.manifest, msg.facts, msg.plan, msg.results
		m.rows = buildRows(m.results, m.expanded)
		m.stage = stageChoosing
		m.status = ""
		// По умолчанию отмечено всё, что требует изменений: чаще всего
		// человек хочет применить план целиком, а не выбирать из него.
		for _, r := range m.results {
			if r.Status == engine.StatusChanges {
				m.chosen[r.Provider] = true
			}
		}
		m.cursorToFirstChange()
		return m, nil

	case stepDoneMsg:
		return m, m.onStepDone(msg)

	case tickMsg:
		if m.stage != stageRunning {
			return m, nil
		}
		// Вывод работающей команды подтягивается на экран, пока она
		// работает: иначе молчащий экран на dist-upgrade выглядит как
		// зависание, и человек жмёт Ctrl-C посреди правки конфига.
		m.progress = m.run.sink.snapshot()
		return m, tickCmd()

	case finishedMsg:
		m.stage = stageDone
		m.report = m.run.report
		m.progress = m.run.sink.snapshot()
		return m, m.afterApply()

	case noteMsg:
		m.status = string(msg)
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *Model) cursorToFirstChange() {
	for i, r := range m.rows {
		if r.kind == rowProvider && r.status == engine.StatusChanges {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}
