package gpu

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vshivtsev-dev/keel/internal/plan"
)

// RevertPlan собирает шаги отката из записи о пробросе.
//
// Откат — это тоже изменение системы, и идти он обязан через те же ворота:
// человек должен видеть каждую команду до того, как она случится. Тем
// более здесь, где ошибка означает хост без картинки.
func RevertPlan(s *State, backups string, sys func(string) string, guestExists bool) (*plan.Plan, error) {
	if sys == nil {
		sys = func(p string) string { return p }
	}
	p := &plan.Plan{Version: plan.FormatVersion, Host: s.VM}

	var prev string
	add := func(step plan.Step) {
		if prev != "" {
			step.Needs = []string{prev}
		}
		p.Steps = append(p.Steps, step)
		prev = step.ID
	}

	// Файлы, существовавшие до нас, — вернуть из копий.
	for _, path := range s.Changed {
		backup := filepath.Join(backups, s.Stamp, path)
		if _, err := os.Stat(backup); err != nil {
			return nil, fmt.Errorf("нет копии для %s (искал %s) — правь руками.\n"+
				"Содержимое файла до правки keel не выдумает", path, backup)
		}
		raw, err := os.ReadFile(backup)
		if err != nil {
			return nil, err
		}
		add(plan.Step{
			ID: "gpu-revert:restore:" + path, Provider: "host/gpu", Resource: path,
			Summary: "вернуть " + path + " из копии",
			Action:  plan.ActionWrite, Path: path, Content: raw,
		})
	}

	// Файлы, которых до нас не было, — удалить.
	for _, path := range s.CreatedFiles {
		if _, err := os.Stat(sys(path)); err != nil {
			continue
		}
		add(plan.Step{
			ID: "gpu-revert:remove:" + path, Provider: "host/gpu", Resource: path,
			Summary: "удалить " + path,
			Action:  plan.ActionExec, Cmd: []string{"rm", "-f", path},
		})
	}

	refresh := []string{"proxmox-boot-tool", "refresh"}
	if s.Bootloader == "grub" || s.Bootloader == "" {
		refresh = []string{"update-grub"}
	}
	add(plan.Step{
		ID: "gpu-revert:boot", Provider: "host/gpu", Resource: "загрузчик",
		Summary: "обновить конфигурацию загрузчика",
		Action:  plan.ActionExec, Cmd: refresh,
	})
	add(plan.Step{
		ID: "gpu-revert:initramfs", Provider: "host/gpu", Resource: "initramfs",
		Summary: "пересобрать initramfs",
		Action:  plan.ActionExec, Cmd: []string{"update-initramfs", "-u", "-k", "all"},
	})

	if guestExists {
		add(plan.Step{
			ID: "gpu-revert:hostpci", Provider: "host/gpu", Resource: "ВМ " + s.VM,
			Summary: "забрать видеокарту у ВМ " + s.VM,
			Action:  plan.ActionExec, Cmd: []string{"qm", "set", s.VM, "--delete", "hostpci0"},
		})
	}
	return p, nil
}
