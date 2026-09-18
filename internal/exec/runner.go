package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Decision — что человек решил сделать с шагом.
type Decision string

const (
	Apply Decision = "apply"
	Skip  Decision = "skip"
	Abort Decision = "abort"
)

// ErrAborted возвращается, когда человек прервал применение. Это не сбой,
// а решение, и обрабатывается оно иначе, чем упавшая команда.
var ErrAborted = fmt.Errorf("применение прервано")

// Runner — единственные ворота, через которые keel меняет систему.
//
// Наследник run() и run_write() из lib/core.sh. Все свойства сохранены:
// команда или diff видны до того, как что-то произойдёт; правленый файл
// сначала копируется; всё попадает в лог; секреты маскируются везде,
// где их мог бы увидеть человек.
//
// Провайдеры сюда не ходят — они описывают шаги, а исполняет движок.
type Runner struct {
	// Out — куда течёт вывод выполняемых команд. Ничего не показывать
	// нельзя: dist-upgrade и скачивание образа длятся минутами, и молчащий
	// экран выглядит как зависание.
	Out io.Writer
	// Log получает то же самое, слово в слово.
	Log io.Writer
	// Mask прячет секреты во всём, что уходит на экран и в лог.
	Mask func(string) string
	// Confirm спрашивает про каждый шаг. nil — не спрашивать: так работает
	// применение уже подтверждённого плана.
	Confirm func(step plan.Step, body string) Decision
	// DryRun показывает, но не выполняет.
	DryRun bool
	// Sandboxed — системные пути уведены в сторону (KEEL_FS_ROOT).
	//
	// В песочнице команды не выполняются, а записываются. Отображать
	// пути и при этом по-настоящему звать pvesm было бы худшим из
	// сочетаний: файлы легли бы во временный каталог, а хост изменился
	// бы взаправду. Записанные команды доступны через Recorded.
	Sandboxed bool
	// BackupDir — куда класть копии файлов перед правкой.
	BackupDir string
	// Stamp помечает все копии одного запуска общей меткой времени.
	Stamp string
	// Interactive отдаёт терминал команде целиком. Возвращает nil, если
	// не умеет, — тогда шаг выполняется обычным образом.
	Interactive func(ctx context.Context, cmd *exec.Cmd) error

	recorded []string
	// Guards — как проверять условия перед шагом. Ключ не найден —
	// условие считается невыполнимым, и шаг пропускается: молча выполнить
	// шаг, условие которого некому проверить, хуже, чем не выполнить.
	Guards map[plan.GuardKind]GuardFunc
	// Sys отображает системные пути. В обычной работе это тождество, но
	// при заданном KEEL_FS_ROOT всё уезжает во временный каталог.
	//
	// Отображение живёт здесь, а не в провайдере, и это важно: провайдер
	// называет настоящий путь — его увидит человек и он же попадёт в план,
	// — а увести запись в сторону от живой машины может только тот, кто
	// эту запись выполняет.
	Sys func(string) string
}

// Recorded — команды, выполненные или записанные в песочнице, по порядку.
func (r *Runner) Recorded() []string { return r.recorded }

func (r *Runner) sys(path string) string {
	if r.Sys == nil {
		return path
	}
	return r.Sys(path)
}

// GuardFunc проверяет условие. Возвращает причину отказа; пустая строка —
// условие выполнено.
type GuardFunc func(ctx context.Context, arg string) string

// ErrGuarded возвращается, когда шаг не выполнен из-за условия. Это не
// сбой: keel отказался начинать, и хост остался цел.
type ErrGuarded struct {
	Step plan.Step
	Why  string
}

func (e *ErrGuarded) Error() string { return e.Why }

// Do выполняет один шаг плана.
func (r *Runner) Do(ctx context.Context, step plan.Step) error {
	body, err := r.body(step)
	if err != nil {
		return err
	}

	r.logf("ШАГ:    %s", step.Summary)
	if body != "" {
		r.logf("ЧТО:    %s", body)
	}

	if r.DryRun {
		r.announce(step, body)
		return nil
	}

	if r.Confirm != nil {
		switch r.Confirm(step, r.mask(body)) {
		case Apply:
		case Skip:
			r.logf("ПРОПУЩЕНО: %s", step.Summary)
			return nil
		default:
			r.logf("ПРЕРВАНО на шаге: %s", step.Summary)
			return ErrAborted
		}
	}

	for _, g := range step.Guards {
		check, ok := r.Guards[g.Kind]
		if !ok {
			return &ErrGuarded{Step: step, Why: "некому проверить условие «" + string(g.Kind) + "»"}
		}
		if why := check(ctx, g.Arg); why != "" {
			r.logf("НЕ НАЧАТО: %s — %s", step.Summary, why)
			return &ErrGuarded{Step: step, Why: why}
		}
	}

	switch step.Action {
	case plan.ActionExec:
		return r.doExec(ctx, step)
	case plan.ActionWrite:
		return r.doWrite(step)
	case plan.ActionMkdir:
		return r.doMkdir(step)
	default:
		return fmt.Errorf("шаг %s: неизвестное действие %q", step.ID, step.Action)
	}
}

// body — то, что показывается человеку до выполнения: команда целиком или
// diff файла.
func (r *Runner) body(step plan.Step) (string, error) {
	switch step.Action {
	case plan.ActionExec:
		if len(step.Cmd) == 0 {
			return "", fmt.Errorf("шаг %s: команда пуста", step.ID)
		}
		return Render(step.Cmd), nil
	case plan.ActionWrite:
		if step.Path == "" {
			return "", fmt.Errorf("шаг %s: не указан путь", step.ID)
		}
		return step.Diff, nil
	case plan.ActionMkdir:
		if step.Path == "" {
			return "", fmt.Errorf("шаг %s: не указан путь", step.ID)
		}
		return "mkdir -p " + step.Path, nil
	}
	return "", fmt.Errorf("шаг %s: неизвестное действие %q", step.ID, step.Action)
}

func (r *Runner) doExec(ctx context.Context, step plan.Step) error {
	r.announce(step, "")
	r.recorded = append(r.recorded, Render(step.Cmd))

	if r.Sandboxed {
		r.logf("В ПЕСОЧНИЦЕ, не выполняю: %s", Render(step.Cmd))
		return nil
	}

	cmd := exec.CommandContext(ctx, step.Cmd[0], step.Cmd[1:]...)

	if step.Interactive && r.Interactive != nil {
		if err := r.Interactive(ctx, cmd); err != nil {
			return fmt.Errorf("%s: %w", Render(step.Cmd), err)
		}
		r.logf("ГОТОВО: %s", step.Summary)
		return nil
	}

	sink := r.sink()
	cmd.Stdout, cmd.Stderr = sink, sink
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		r.logf("ОШИБКА: %s — %v", Render(step.Cmd), err)
		return fmt.Errorf("%s: %w", Render(step.Cmd), err)
	}
	r.logf("ГОТОВО: %s", step.Summary)
	return nil
}

func (r *Runner) doWrite(step plan.Step) error {
	r.announce(step, "")
	target := r.sys(step.Path)
	if err := r.backup(step.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("каталог для %s: %w", step.Path, err)
	}
	body := step.Content
	// Конфиг без перевода строки в конце — источник тихих сюрпризов.
	if len(body) > 0 && body[len(body)-1] != '\n' {
		body = append(append([]byte(nil), body...), '\n')
	}
	if err := os.WriteFile(target, body, 0o644); err != nil {
		return fmt.Errorf("запись %s: %w", step.Path, err)
	}
	r.logf("ЗАПИСАНО: %s", step.Path)
	return nil
}

func (r *Runner) doMkdir(step plan.Step) error {
	r.announce(step, "")
	if err := os.MkdirAll(r.sys(step.Path), 0o755); err != nil {
		return fmt.Errorf("создание каталога %s: %w", step.Path, err)
	}
	r.logf("СОЗДАН КАТАЛОГ: %s", step.Path)
	return nil
}

// backup снимает копию файла перед правкой. Делается всегда и молча —
// это не изменение состояния, а страховка.
func (r *Runner) backup(path string) error {
	if r.BackupDir == "" {
		return nil
	}
	raw, err := os.ReadFile(r.sys(path))
	if err != nil {
		return nil // файла ещё нет — копировать нечего
	}
	stamp := r.Stamp
	if stamp == "" {
		stamp = time.Now().Format("2006-01-02_150405")
	}
	dest := filepath.Join(r.BackupDir, stamp, path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("каталог копий %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, raw, 0o600); err != nil {
		return fmt.Errorf("копия %s: %w", path, err)
	}
	r.logf("КОПИЯ: %s -> %s", path, dest)
	return nil
}

func (r *Runner) announce(step plan.Step, body string) {
	if r.Out == nil {
		return
	}
	fmt.Fprintf(r.Out, "→ %s\n", r.mask(step.Summary))
	if body != "" {
		fmt.Fprintf(r.Out, "    %s\n", r.mask(body))
	}
}

func (r *Runner) sink() io.Writer {
	switch {
	case r.Out != nil && r.Log != nil:
		return io.MultiWriter(r.Out, r.Log)
	case r.Out != nil:
		return r.Out
	case r.Log != nil:
		return r.Log
	default:
		return io.Discard
	}
}

func (r *Runner) mask(s string) string {
	if r.Mask == nil {
		return s
	}
	return r.Mask(s)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log == nil {
		return
	}
	fmt.Fprintf(r.Log, "%s\n", r.mask(fmt.Sprintf(format, args...)))
}
