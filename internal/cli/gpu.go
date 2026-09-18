package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/gpu"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/provider/host"
)

// confirmBlackout — подтверждение, которое нельзя нажать не глядя.
//
// Обычное «да/нет» здесь бесполезно: человек, прошедший десяток
// подтверждений подряд, нажмёт Enter и на этом. Поэтому надо набрать
// адрес устройства — ровно тот, что уедет в ВМ.
func (a *App) confirmBlackout(m *manifest.Manifest, f *facts.Facts) error {
	if m.Host.GPUPassthrough == nil {
		return nil
	}
	g := host.GPU{Paths: a.Paths, Capturer: a.Capturer}
	t, err := g.Resolve(context.Background(), m, f)
	if err != nil {
		return nil // о причине скажет сам провайдер при сборке плана
	}
	if !t.OnlyCard {
		return nil
	}

	fmt.Fprintf(a.Out, `
ХОСТ ОСТАНЕТСЯ БЕЗ МОНИТОРА

Видеокарта %s на этом хосте одна.

После перезагрузки локальный монитор погаснет НАВСЕГДА. Управление
останется только через веб-интерфейс и SSH. Если ВМ не поднимется,
чинить придётся вслепую или с live-USB.

Чтобы подтвердить, набери адрес устройства: %s
`, t.Address, t.Address)

	answer, err := a.askHidden("Адрес: ")
	if err != nil {
		return fmt.Errorf("подтвердить проброс некому: нет терминала")
	}
	if strings.TrimSpace(answer) != t.Address {
		return fmt.Errorf("не подтверждено — проброс не выполняется")
	}
	return nil
}

// recordGPUState записывает, что именно keel изменил, отдавая видеокарту.
//
// Это единственное место, где keel пишет файл мимо плана, и так задумано:
// запись должна отражать то, что случилось на самом деле, а не то, что
// планировалось. Без неё откат невозможен, а откат здесь — вопрос того,
// увидит ли человек снова картинку на мониторе.
func (a *App) recordGPUState(m *manifest.Manifest, f *facts.Facts, rep engine.ApplyReport, stamp string) error {
	var changed, created []string
	for _, s := range rep.Done {
		if s.Provider != "host/gpu" || s.Action != plan.ActionWrite {
			continue
		}
		// Копия снята только у файла, который существовал до нас. Всё
		// остальное keel создал сам и при откате должен удалить.
		if _, err := os.Stat(a.Paths.Sys(s.Path)); err == nil && hadBackup(a.Paths.Backups(), stamp, s.Path) {
			changed = append(changed, s.Path)
		} else {
			created = append(created, s.Path)
		}
	}
	if len(changed) == 0 && len(created) == 0 {
		return nil
	}

	g := host.GPU{Paths: a.Paths, Capturer: a.Capturer}
	t, err := g.Resolve(context.Background(), m, f)
	if err != nil {
		return err
	}

	state := &gpu.State{
		Created: time.Now(), Device: t.Address, IDs: t.IDs, VM: t.VM,
		Stamp: stamp, Bootloader: f.Bootloader,
		Changed: changed, CreatedFiles: created,
	}
	if err := gpu.Save(a.Paths.Home(), state); err != nil {
		return err
	}

	s := newSheet(a.Out)
	s.section("Как вернуть всё назад")
	fmt.Fprintln(a.Out, gpu.RevertInstructions(a.Paths.Home(), a.Paths.Backups(),
		"/etc/modprobe.d/keel-vfio.conf"))
	s.warn("перепиши это", "с погасшего монитора четыре строки не прочитать")
	return nil
}

func hadBackup(backups, stamp, path string) bool {
	_, err := os.Stat(backups + "/" + stamp + path)
	return err == nil
}

// GPURevert возвращает хост в состояние до проброса.
func GPURevert(ctx context.Context, a *App) error {
	if err := NeedRoot(); err != nil {
		return err
	}
	state, err := gpu.Load(a.Paths.Home())
	if err != nil {
		return err
	}

	m, err := a.LoadManifest()
	if err != nil {
		return err
	}
	f := a.FactsFor(ctx, m)

	s := newSheet(a.Out)
	s.section("Откат проброса видеокарты")
	s.row("устройство", state.Device)
	s.row("ВМ", state.VM)
	s.row("сделано", state.Created.Format("2006-01-02 15:04:05"))

	vm, _ := strconv.Atoi(state.VM)
	p, err := gpu.RevertPlan(state, a.Paths.Backups(), a.Paths.Sys, f.GuestExists(vm))
	if err != nil {
		return err
	}

	runner := &exec.Runner{
		Out: a.Out, Log: a.Log, Mask: a.Masker.Apply,
		DryRun:    a.Opts.Mode == ModeDry,
		BackupDir: a.Paths.Backups(),
		Stamp:     time.Now().Format("2006-01-02_150405"),
		Sys:       a.Paths.Sys,
		Sandboxed: a.Paths.Sandboxed(),
	}
	if a.Opts.Mode == ModeStep {
		runner.Confirm = askAboutStep(a)
	}

	rep := engine.Apply(ctx, runner, p)
	report(a, s, rep)
	if !rep.Ok() {
		return fmt.Errorf("откат выполнен не полностью — подробности в логе: %s", a.Log.Path())
	}
	if a.Opts.Mode != ModeDry {
		if err := gpu.Remove(a.Paths.Home()); err != nil {
			return err
		}
	}
	s.ok("готово", "перезагрузи хост, чтобы видеокарта вернулась драйверу")
	return nil
}

// GPUStatus показывает, что записано о пробросе.
func GPUStatus(a *App) error {
	state, err := gpu.Load(a.Paths.Home())
	if err != nil {
		fmt.Fprintln(a.Out, "Записи о пробросе нет — видеокарта не отдавалась.")
		return nil
	}
	s := newSheet(a.Out)
	s.section("Проброс видеокарты")
	s.row("устройство", state.Device+" ("+state.IDs+")")
	s.row("ВМ", state.VM)
	s.row("сделано", state.Created.Format("2006-01-02 15:04:05"))
	s.row("загрузчик", state.Bootloader)
	if len(state.Changed) > 0 {
		s.row("изменено", strings.Join(state.Changed, ", "))
	}
	if len(state.CreatedFiles) > 0 {
		s.row("создано", strings.Join(state.CreatedFiles, ", "))
	}
	s.row("откатить", "keel gpu revert")
	return nil
}
