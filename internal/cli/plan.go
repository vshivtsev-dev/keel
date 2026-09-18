package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Plan собирает план и сохраняет его файлом. Ничего не выполняет.
//
// Сохранение — не мелочь: именно этот файл потом исполнит apply. Раньше
// plan и apply считали изменения независимо, и между ними состояние хоста
// могло поменяться — посмотрел одно, применилось другое.
func Plan(ctx context.Context, a *App) error {
	m, err := a.LoadManifest()
	if err != nil {
		return err
	}
	eng, err := a.Engine()
	if err != nil {
		return err
	}

	p, results := eng.Collect(ctx, m, a.Facts(ctx))

	if a.Opts.JSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	if a.Opts.Commands {
		printCommands(a.Out, p)
		return nil
	}

	s := newSheet(a.Out)
	s.section("План изменений")
	s.note("Режим просмотра: ничего не выполняется.")

	printResults(s, results)

	if p.Empty() {
		fmt.Fprintln(a.Out)
		// Пустой манифест и совпавший хост — это разные вещи, и говорить
		// о них одинаково нельзя: в первом случае человек, скорее всего,
		// просто ещё не описал, что хочет.
		if allUnconfigured(results) {
			s.ok("итог", "в манифесте ничего не описано — keel не тронет ничего")
			s.note("что писать в манифесте: docs/MANIFEST.md")
			return nil
		}
		s.ok("итог", "менять нечего — хост уже такой, как описан")
		return nil
	}

	path, err := p.Save(a.Paths.Plans())
	if err != nil {
		return err
	}

	fmt.Fprintln(a.Out)
	s.section("Что дальше")
	s.row("план сохранён", path)
	s.row("применить его", "keel apply")
	s.note("apply выполнит именно этот план и откажется, если хост успеет измениться")
	return nil
}

// printResults печатает состояние каждого провайдера. Три разных значка
// у трёх разных вещей, и путать их нельзя:
//
//	→ есть что менять
//	✓ уже в нужном состоянии
//	· в манифесте про это не сказано — правило нуля, это норма
//	✗ не удалось разобраться в состоянии
func printResults(s *sheet, results []engine.Result) {
	var changes, steps, ok, unconfigured, failed int

	for _, r := range results {
		switch r.Status {
		case engine.StatusChanges:
			changes++
			steps += len(r.Steps)
			s.change(r.Provider, r.Title)
			for _, st := range r.Steps {
				s.indent(st.Summary)
			}
		case engine.StatusOK:
			ok++
			s.ok(r.Provider, r.Title)
		case engine.StatusUnconfigured:
			unconfigured++
			s.skip(r.Provider, r.Title)
		case engine.StatusFailed:
			failed++
			s.bad(r.Provider, r.Title)
			s.indent(r.Err.Error())
		}
		// Заметки печатаются при любом состоянии: это то, что keel видит,
		// но сделать не может, и молчать об этом нельзя.
		for _, n := range r.Notes {
			s.indent("! " + n.Message)
		}
	}

	fmt.Fprintln(s.w)
	// Считаем и области, и шаги: «1 изменение» под тремя строками читается
	// как ошибка в подсчёте, а не как «один провайдер, три шага».
	s.row("итог", fmt.Sprintf("%s в %s · уже в порядке: %d · не настроено: %d · ошибок: %d",
		plural(steps, "шаг", "шага", "шагов"),
		plural(changes, "области", "областях", "областях"), ok, unconfigured, failed))
	if unconfigured > 0 {
		s.note("«не настроено» значит: нет соответствующего ключа в манифесте. Это нормально.")
	}
}

// Verify сверяет хост с манифестом, ничего не меняя.
func Verify(ctx context.Context, a *App) error {
	m, err := a.LoadManifest()
	if err != nil {
		return err
	}
	eng, err := a.Engine()
	if err != nil {
		return err
	}

	results := eng.Verify(ctx, m, a.Facts(ctx))

	if a.Opts.JSON {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	s := newSheet(a.Out)
	s.section("Проверка состояния")

	for _, r := range results {
		switch r.Status {
		case engine.StatusUnconfigured:
			s.skip(r.Provider, r.Title)
			continue
		case engine.StatusFailed:
			s.bad(r.Provider, r.Title)
			s.indent(r.Err.Error())
			continue
		case engine.StatusOK:
			s.ok(r.Provider, r.Title)
		default:
			s.change(r.Provider, r.Title)
		}
		for _, f := range r.Findings {
			s.indent(findingLine(f))
		}
	}
	return nil
}

// printCommands печатает то, что уйдёт хосту, — по команде на строку и
// ничего больше. Нужно, чтобы план можно было прочитать глазами целиком,
// вычитать его скриптом и сверить с тем, что делала bash-версия.
func printCommands(w io.Writer, p *plan.Plan) {
	for _, s := range p.Steps {
		switch s.Action {
		case plan.ActionExec:
			fmt.Fprintln(w, exec.Render(s.Cmd))
		case plan.ActionMkdir:
			fmt.Fprintln(w, "mkdir -p "+s.Path)
		case plan.ActionWrite:
			// Записи файла соответствующей команды нет — показываем путь
			// так, чтобы строку нельзя было спутать с исполнимой командой.
			fmt.Fprintln(w, "# записать "+s.Path)
		}
	}
}

func allUnconfigured(results []engine.Result) bool {
	for _, r := range results {
		if r.Status != engine.StatusUnconfigured {
			return false
		}
	}
	return len(results) > 0
}

func findingLine(f plan.Finding) string {
	switch {
	case f.Unmanaged:
		return "· " + f.Resource + ": " + f.Message
	case f.OK:
		return "✓ " + f.Resource + ": " + f.Message
	default:
		return "→ " + f.Resource + ": " + f.Message
	}
}
