package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/paths"
)

// Doctor печатает отчёт о хосте. Ничего не меняет и не требует прав root:
// это первая команда, которую человек запускает на незнакомом хосте.
func Doctor(ctx context.Context, w io.Writer, p paths.Paths, c exec.Capturer, asJSON bool) error {
	f := facts.Collect(ctx, p, c)

	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(f)
	}

	s := newSheet(w)

	s.section("Хост")
	s.row("имя", or(f.Hostname, "неизвестно"))
	if f.IsPVE {
		s.ok("Proxmox VE", or(f.PVEVersion, "версия неизвестна"))
	} else {
		s.warn("Proxmox VE", "не найден — keel рассчитан на него")
	}
	s.row("Debian", or(f.Codename, "неизвестно"))
	s.row("репозитории apt", f.RepoStyle)
	s.row("загрузчик", f.Bootloader)
	if p.Sandboxed() {
		s.warn("песочница", "KEEL_FS_ROOT="+p.FSRoot()+" — системные пути уведены в сторону")
	}

	s.section("Процессор и IOMMU")
	s.row("производитель", or(f.CPUVendor, "неизвестно"))
	s.row("модель", or(f.CPUModel, "неизвестно"))
	if f.IOMMU {
		s.ok("IOMMU", "включён")
	} else {
		s.warn("IOMMU", "выключен — проброс видеокарты недоступен")
	}

	s.section("Видео")
	if len(f.GPUs) == 0 {
		s.note("видеоустройств не найдено (нет lspci?)")
	}
	for _, g := range f.GPUs {
		s.row(g.Address, fmt.Sprintf("%s — драйвер %s", g.Desc, g.Driver))
	}
	if len(f.DRINodes) > 0 {
		s.row("узлы /dev/dri", strings.Join(f.DRINodes, " "))
	} else {
		s.note("узлов /dev/dri нет — рабочий стол в контейнере не выйдет")
	}

	s.section("Хранилища")
	if len(f.Storages) == 0 {
		s.note("не прочитано (нет /etc/pve/storage.cfg?)")
	}
	for _, st := range f.Storages {
		content := strings.Join(st.Content, ",")
		s.row(st.Name, strings.TrimSpace(fmt.Sprintf("%s  %s  %s", st.Type, content, st.Path)))
	}

	s.section("Гости")
	if len(f.Guests) == 0 {
		s.note("на хосте нет ни одного гостя")
	}
	for _, g := range f.Guests {
		kind := "ВМ"
		if g.Kind == "lxc" {
			kind = "контейнер"
		}
		s.row(fmt.Sprint(g.ID), fmt.Sprintf("%s  %s", kind, or(g.Name, "—")))
	}

	s.section("Сеть и пакеты")
	if len(f.Bridges) > 0 {
		s.row("мосты", strings.Join(f.Bridges, " "))
	} else {
		s.note("мостов не найдено")
	}
	switch {
	case f.Upgradable > 0:
		s.row("ждут обновления", plural(f.Upgradable, "пакет", "пакета", "пакетов"))
	default:
		s.ok("пакеты", "обновлять нечего")
	}

	s.section("Где что лежит")
	s.row("манифест", p.Manifest())
	s.row("секреты", p.Secrets())
	s.row("планы", p.Plans())
	s.row("логи", p.Logs())
	return nil
}

func or(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
