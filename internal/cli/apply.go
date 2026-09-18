package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Apply исполняет сохранённый план.
//
// Без аргумента берётся последний собранный. Перед исполнением план
// сверяется с нынешним состоянием: если хост или манифест успели
// измениться, применять его вслепую нельзя — человек подтверждал другое.
func Apply(ctx context.Context, a *App, planPath string) error {
	if err := NeedRoot(); err != nil {
		return err
	}

	m, err := a.LoadManifest()
	if err != nil {
		return err
	}
	f := a.FactsFor(ctx, m)
	if err := NeedPVE(f, a.Paths); err != nil {
		return err
	}

	p, err := loadOrCollect(ctx, a, planPath, m, f)
	if err != nil {
		return err
	}

	s := newSheet(a.Out)
	if p.Empty() {
		s.ok("нечего делать", "хост уже такой, как описан")
		return nil
	}

	if err := checkStale(a, s, p, m.Path, f); err != nil {
		return err
	}

	runner := &exec.Runner{
		Out:       a.Out,
		Log:       a.Log,
		Mask:      a.Masker.Apply,
		DryRun:    a.Opts.Mode == ModeDry,
		BackupDir: a.Paths.Backups(),
		Sys:       a.Paths.Sys,
		Sandboxed: a.Paths.Sandboxed(),
		Guards:    guards(f),
		Resolve:   a.Secrets.Resolve,
		Stamp:     time.Now().Format("2006-01-02_150405"),
	}
	if a.Opts.Mode == ModeStep {
		runner.Confirm = askAboutStep(a)
	}

	// Точка невозврата: дальше устройство уедет от хоста.
	if a.Opts.Mode != ModeDry {
		if err := a.confirmBlackout(m, f); err != nil {
			return err
		}
	}

	s.section("Применение")
	if a.Paths.Sandboxed() {
		s.warn("песочница", "KEEL_FS_ROOT="+a.Paths.FSRoot()+" — файлы уводятся в сторону, команды не выполняются")
	}
	rep := engine.Apply(ctx, runner, p)
	report(a, s, rep)

	if a.Opts.Mode != ModeDry {
		if err := a.recordGPUState(m, f, rep, runner.Stamp); err != nil {
			s.warn("запись о пробросе", err.Error())
		}
	}

	switch {
	case rep.Aborted:
		return fmt.Errorf("прервано")
	case len(rep.Failed) > 0:
		return fmt.Errorf("не удалось выполнить %s — подробности в логе: %s",
			plural(len(rep.Failed), "шаг", "шага", "шагов"), a.Log.Path())
	}
	return nil
}

// loadOrCollect берёт план из файла или собирает свежий. Собрать на месте
// разрешено нарочно: `keel apply` сразу после установки не должен требовать
// отдельного `keel plan` — иначе первый же шаг восстановления упрётся в
// ритуал.
func loadOrCollect(ctx context.Context, a *App, planPath string, m *manifest.Manifest, f *facts.Facts) (*plan.Plan, error) {
	if planPath == "" {
		if latest, err := plan.Latest(a.Paths.Plans()); err == nil {
			planPath = latest
		}
	}
	if planPath != "" {
		p, err := plan.LoadFile(planPath)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(a.Out, "План: %s\n", planPath)
		return p, nil
	}

	eng, err := a.Engine()
	if err != nil {
		return nil, err
	}
	p, results := eng.Collect(ctx, m, f)
	printResults(newSheet(a.Out), results)
	return p, nil
}

// checkStale не даёт применить план, разошедшийся с действительностью.
func checkStale(a *App, s *sheet, p *plan.Plan, manifestPath string, f *facts.Facts) error {
	stale, why := p.Stale(engine.ManifestDigest(manifestPath), f.Digest())
	if !stale {
		return nil
	}
	if a.Opts.Stale {
		s.warn("план устарел", why+" — применяю по требованию (--stale)")
		return nil
	}
	return fmt.Errorf("план устарел: %s.\n"+
		"Собери заново: keel plan. Применить этот всё равно: keel apply --stale", why)
}

// askAboutStep спрашивает про каждое изменение. Вопрос задаётся терминалу
// напрямую: вывод команды может быть перенаправлен, а спрашивать надо всё
// равно у человека.
func askAboutStep(a *App) func(plan.Step, string) exec.Decision {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		tty = os.Stdin
	}
	reader := bufio.NewReader(tty)

	return func(step plan.Step, body string) exec.Decision {
		fmt.Fprintf(a.Out, "\n→ %s\n", a.Masker.Apply(step.Summary))
		if body != "" {
			for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				fmt.Fprintf(a.Out, "    %s\n", line)
			}
		}
		for _, u := range step.Unknown {
			fmt.Fprintf(a.Out, "    ? станет известно при выполнении: %s\n", u)
		}
		fmt.Fprint(a.Out, "Enter — применить, s — пропустить, q — прервать: ")

		answer, err := reader.ReadString('\n')
		if err != nil {
			// Не удалось спросить — самое безопасное решение остановиться,
			// а не применить всё подряд.
			return exec.Abort
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "д", "p", "з":
			return exec.Apply
		case "s", "ы":
			return exec.Skip
		default:
			return exec.Abort
		}
	}
}

func report(a *App, s *sheet, rep engine.ApplyReport) {
	fmt.Fprintln(a.Out)
	s.section("Итог")
	// В режиме показа шаги не выполнялись, и говорить «выполнено» нельзя:
	// именно так и рождается уверенность, что хост уже настроен.
	if a.Opts.Mode == ModeDry {
		s.row("показано", plural(len(rep.Done), "шаг", "шага", "шагов"))
		s.note("ничего не выполнено: это режим показа")
		return
	}
	s.row("выполнено", plural(len(rep.Done), "шаг", "шага", "шагов"))
	if len(rep.Skipped) > 0 {
		s.warn("пропущено", plural(len(rep.Skipped), "шаг", "шага", "шагов")+" — ждали упавшего")
		for _, st := range rep.Skipped {
			s.indent(st.Summary)
		}
	}
	// Не начатое по условию — это не сбой: keel отказался начинать, и хост
	// остался цел. Мешать это со сбоями значило бы пугать там, где keel
	// как раз сработал правильно.
	for _, ge := range rep.Guarded {
		s.warn("не начато", ge.Step.Summary)
		s.indent(ge.Err.Error())
	}
	for _, fe := range rep.Failed {
		s.bad("не удалось", fe.Step.Summary)
		s.indent(fe.Err.Error())
	}
	if rep.Aborted {
		s.warn("прервано", "по твоему решению")
	}
	if path := a.Log.Path(); path != "" {
		s.row("лог", path)
	}
}
