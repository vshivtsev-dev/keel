package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/profile"
)

// Password показывает пароль, сохранённый для гостя.
//
// В манифесте паролей нет и не будет: манифест человек правит руками и
// кладёт в git. Пароли живут отдельными файлами с правами 0600, и это
// единственный способ их посмотреть.
func Password(a *App, id string) error {
	if id == "" {
		return fmt.Errorf("укажи id гостя: keel password 102")
	}
	value, err := a.Secrets.Get(id)
	if err != nil {
		return fmt.Errorf("%w\nОн появится, когда keel создаст гостя", err)
	}
	fmt.Fprintln(a.Out, value)
	return nil
}

// Token показывает сохранённый токен внешней службы.
func Token(a *App, name string) error {
	if name == "" {
		name = "cloudflared"
	}
	value, err := a.Secrets.Get(name)
	if err != nil {
		return fmt.Errorf("%w\nПоложи его туда одной строкой или дай keel спросить при применении", err)
	}
	fmt.Fprintln(a.Out, value)
	return nil
}

// Guests печатает гостей из манифеста и их состояние на хосте.
//
// Машины и контейнеры разведены нарочно: в Proxmox это разные сущности с
// разными командами и разной ценой ошибки, и в одном списке они выглядят
// обманчиво одинаково.
func Guests(ctx context.Context, a *App) error {
	m, err := a.LoadManifest()
	if err != nil {
		return err
	}
	f := a.FactsFor(ctx, m)
	s := newSheet(a.Out)

	type entry struct{ kind, line, state string }
	var rows []entry

	for _, g := range m.Guests {
		kind, title := "неизвестно", "профиль не разобран"
		if name, err := profile.Resolve(g.Profile, g.Graphics); err == nil && name != "" {
			if p, err := profile.Load(name); err == nil {
				title = p.Title
				kind = "vm"
				if p.Kind == profile.KindLXC {
					kind = "lxc"
				}
			}
		}
		state := "нет на хосте"
		if f.GuestExists(g.ID) {
			state = "уже есть, не трону"
		}
		// pad считает символы, а не байты: названия профилей русские, и
		// %-30s разъехалось бы на них колонками.
		rows = append(rows, entry{kind,
			fmt.Sprintf("%-5d %s %s", g.ID, pad(or(g.Name, "без имени"), 16), pad(title, 32)),
			state})
	}

	for _, want := range []struct{ kind, label string }{
		{"vm", "Виртуальные машины"},
		{"lxc", "Контейнеры LXC"},
	} {
		s.section(want.label)
		found := false
		for _, r := range rows {
			if r.kind != want.kind {
				continue
			}
			found = true
			s.row(r.line, r.state)
		}
		if !found {
			s.note("в манифесте таких нет")
		}
	}

	// Гость вне манифеста keel не тронет никогда — но расскажет о нём.
	described := map[int]bool{}
	for _, g := range m.Guests {
		described[g.ID] = true
	}
	var unmanaged []string
	for _, g := range f.Guests {
		if !described[g.ID] {
			unmanaged = append(unmanaged, fmt.Sprintf("%d «%s»", g.ID, or(g.Name, "—")))
		}
	}
	if len(unmanaged) > 0 {
		s.section("Есть на хосте, но не в манифесте")
		s.note("keel их не трогает: " + strings.Join(unmanaged, ", "))
	}
	return nil
}

// Logs печатает последний лог. Когда что-то пошло не так на хосте без
// монитора, этот файл — единственное, что остаётся.
func Logs(a *App) error {
	dir := a.Paths.Logs()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("логов пока нет (%s)", dir)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("логов пока нет (%s)", dir)
	}
	// Имя лога — метка времени, поэтому алфавитный порядок совпадает
	// с хронологическим.
	sort.Strings(names)

	// Последний лог — это текущий запуск, в котором пока только запись о
	// старте. Человек спрашивает про предыдущий.
	pick := names[len(names)-1]
	if len(names) > 1 {
		pick = names[len(names)-2]
	}
	raw, err := os.ReadFile(filepath.Join(dir, pick))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s\n\n", filepath.Join(dir, pick))
	fmt.Fprint(a.Out, string(raw))
	return nil
}
