package cli

import (
	"fmt"
	"io"

	"github.com/vshivtsev-dev/keel/internal/manifest"
)

// Validate читает манифест и рассказывает, что в нём описано.
//
// Пустой манифест — это не ошибка, а осознанное «не трогай ничего»:
// правило нуля работает и здесь.
func Validate(w io.Writer, path string) error {
	m, err := manifest.Load(path)
	if err != nil {
		return err
	}

	s := newSheet(w)
	s.section("Манифест")
	s.row("файл", m.Path)

	s.section("Хост")
	described := 0
	if m.Host.Repos != "" {
		s.row("репозитории", m.Host.Repos)
		described++
	}
	if m.Host.Updates != nil {
		s.row("обновления", yesno(*m.Host.Updates))
		described++
	}
	if m.Host.ConfigBackup != nil {
		s.row("копия конфигурации", fmt.Sprintf("%s, хранить %d",
			m.Host.ConfigBackup.Path, m.Host.ConfigBackup.KeepOr()))
		described++
	}
	if m.Host.GPUPassthrough != nil {
		s.row("проброс видеокарты", fmt.Sprintf("в ВМ %s, устройство %s",
			m.Host.GPUPassthrough.VM, m.Host.GPUPassthrough.DeviceOr()))
		described++
	}
	if described == 0 {
		s.note("про хост в манифесте ничего не сказано — keel его не тронет")
	}

	s.section("Хранилища")
	if len(m.Storages) == 0 {
		s.note("не описаны — keel их не тронет")
	}
	for _, st := range m.Storages {
		s.row(st.Name, describeStorage(st))
	}

	s.section("Гости")
	if len(m.Guests) == 0 {
		s.note("не описаны — keel их не создаст")
	}
	for _, g := range m.Guests {
		s.row(fmt.Sprint(g.ID), fmt.Sprintf("%s  профиль %s", or(g.Name, "без имени"), or(g.Profile, "—")))
	}

	s.section("Резервное копирование")
	if m.Backup == nil {
		s.note("не описано — keel задание не заведёт")
	} else {
		s.row("расписание", m.Backup.Schedule)
		s.row("хранилище", m.Backup.StorageOr())
		s.row("режим", m.Backup.ModeOr())
		s.row("гостей в задании", plural(len(m.Backup.Guests), "гость", "гостя", "гостей"))
	}

	fmt.Fprintln(w)
	return nil
}

func describeStorage(st manifest.Storage) string {
	out := ""
	if st.Type != "" {
		out += st.Type + " "
	}
	if st.Path != "" {
		out += st.Path + " "
	}
	if len(st.Content) > 0 {
		out += "content=" + join(st.Content)
	}
	return or(out, "—")
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func yesno(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}
