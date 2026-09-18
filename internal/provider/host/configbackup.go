package host

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/ru"
)

// ConfigBackup складывает конфигурацию самого хоста в архив.
//
// Порт modules/host/90-config-backup.sh. Бэкапы виртуалок делает vzdump.
// А вот сам хост — его сеть, хранилища, список гостей, ключи — не бэкапит
// никто. Именно этого и не хватает, когда всё сгорело: гости-то
// восстановятся, а куда их класть и как называется мост — уже нет.
//
// Копия — обычный tar.gz, который читается чем угодно.
type ConfigBackup struct {
	// Now подменяется в тестах, чтобы имя архива было предсказуемым.
	Now func() time.Time
}

func (ConfigBackup) ID() string { return "host/config-backup" }
func (ConfigBackup) Title() string {
	return "Резервная копия конфигурации хоста"
}

func (ConfigBackup) Describe() string {
	return `Складывает в архив /etc/pve, настройки сети, хранилищ, apt, загрузчика и
текстовый снимок состояния (версии, диски, список гостей). Старые архивы
прореживает, оставляя указанное количество. Ничего в системе не меняет,
кроме собственного каталога с архивами.`
}

func (ConfigBackup) Configured(m *manifest.Manifest) bool {
	return m.Host.ConfigBackup != nil && m.Host.ConfigBackup.Path != ""
}

func (cb ConfigBackup) now() time.Time {
	if cb.Now != nil {
		return cb.Now()
	}
	return time.Now()
}

func (cb ConfigBackup) Plan(_ context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	want := m.Host.ConfigBackup
	dir := want.Path
	maxAge := time.Duration(want.MaxAgeHoursOr()) * time.Hour

	var out plan.Changes

	// Свежая копия уже есть — делать нечего.
	if len(f.ConfigArchives) > 0 && f.ConfigArchives[0].Age < maxAge {
		return out, nil
	}

	paths := f.ConfigPaths(f.SysForPaths)
	if len(paths) == 0 {
		return out, fmt.Errorf("не нашлось ни одного файла конфигурации — это точно хост Proxmox?")
	}

	stamp := cb.now().Format("2006-01-02_150405")
	archive := path.Join(dir, "keel-host-"+stamp+".tar.gz")
	// Каталог сборки лежит рядом с архивом, а не в /tmp: так он заведомо
	// на той же файловой системе, и его видно, если что-то пошло не так.
	staging := path.Join(dir, ".keel-staging-"+stamp)

	mkdirID := cb.ID() + ":mkdir"
	out.Steps = append(out.Steps, plan.Step{
		ID: mkdirID, Provider: cb.ID(), Resource: "каталог копий",
		Summary: "создать каталог для копий " + dir,
		Action:  plan.ActionMkdir, Path: dir,
	})

	// Снимок состояния и манифест кладутся рядом с файлами: без них архив —
	// просто набор конфигов без объяснения, от какой он машины.
	extras := []struct{ name, body string }{
		{"host-report.txt", f.Report},
		{"keel-version.txt", fmt.Sprintf("снято %s\n", cb.now().Format("2006-01-02 15:04:05"))},
	}
	var extraIDs []string
	for _, e := range extras {
		id := cb.ID() + ":staging:" + e.name
		extraIDs = append(extraIDs, id)
		out.Steps = append(out.Steps, plan.Step{
			ID: id, Provider: cb.ID(), Resource: "снимок состояния",
			Summary: "положить в архив " + e.name,
			Action:  plan.ActionWrite, Path: path.Join(staging, e.name),
			Content: []byte(e.body),
			Needs:   []string{mkdirID},
		})
	}
	if m.Path != "" {
		id := cb.ID() + ":staging:manifest.json"
		extraIDs = append(extraIDs, id)
		out.Steps = append(out.Steps, plan.Step{
			ID: id, Provider: cb.ID(), Resource: "снимок состояния",
			Summary: "положить в архив копию манифеста",
			Action:  plan.ActionExec,
			Cmd:     []string{"cp", m.Path, path.Join(staging, "manifest.json")},
			Needs:   []string{mkdirID},
		})
	}

	tarID := cb.ID() + ":tar"
	tarCmd := []string{"tar", "-czf", archive, "--ignore-failed-read", "-C", staging, "."}
	// Корень берём отображённый: в песочнице архив должен собираться из
	// неё, а не из живой машины.
	tarCmd = append(tarCmd, "-C", sysRoot(f))
	tarCmd = append(tarCmd, paths...)
	out.Steps = append(out.Steps, plan.Step{
		ID: tarID, Provider: cb.ID(), Resource: "архив",
		Summary: fmt.Sprintf("собрать архив конфигурации %s (%s)",
			path.Base(archive), ru.Path(len(paths))),
		Action: plan.ActionExec, Cmd: tarCmd,
		Needs: append([]string{mkdirID}, extraIDs...),
	})

	out.Steps = append(out.Steps, plan.Step{
		ID: cb.ID() + ":cleanup", Provider: cb.ID(), Resource: "каталог сборки",
		Summary: "убрать временный каталог сборки",
		Action:  plan.ActionExec, Cmd: []string{"rm", "-rf", staging},
		Needs: []string{tarID},
	})

	// Прореживание: оставляем только свежие копии. Считаем от списка,
	// который уже есть, плюс тот архив, что соберётся сейчас.
	keep := want.KeepOr()
	for i, old := range f.ConfigArchives {
		if i+1 < keep {
			continue
		}
		out.Steps = append(out.Steps, plan.Step{
			ID: cb.ID() + ":prune:" + path.Base(old.Path), Provider: cb.ID(),
			Resource: "старая копия",
			Summary:  "удалить устаревшую копию " + path.Base(old.Path),
			Action:   plan.ActionExec, Cmd: []string{"rm", "-f", old.Path},
			Needs: []string{tarID},
		})
	}

	out.Notes = append(out.Notes, plan.Note{
		Resource: "архив",
		Message:  "держи копию не только на этом хосте — копия рядом с оригиналом спасает лишь от опечаток",
	})
	return out, nil
}

func (cb ConfigBackup) Verify(_ context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	maxAge := time.Duration(m.Host.ConfigBackup.MaxAgeHoursOr()) * time.Hour

	if len(f.ConfigArchives) == 0 {
		return []plan.Finding{{Provider: cb.ID(), Resource: "копия конфигурации",
			Message: "копий конфигурации нет"}}, nil
	}
	latest := f.ConfigArchives[0]
	hours := int(latest.Age.Hours())
	msg := fmt.Sprintf("последняя копия: %s (%s назад)",
		path.Base(latest.Path), ru.Hour(hours))

	return []plan.Finding{{Provider: cb.ID(), Resource: "копия конфигурации",
		Message: msg, OK: latest.Age < maxAge}}, nil
}

func sysRoot(f *facts.Facts) string {
	if f.SysForPaths == nil {
		return "/"
	}
	return f.SysForPaths("/")
}
