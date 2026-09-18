// Package host — провайдеры, приводящие в порядок сам хост Proxmox.
package host

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Storage доводит хранилища до описанного в манифесте.
//
// Порт modules/host/30-storage.sh. Правила те же, и они важнее кода:
//
//	· у существующего хранилища типы content ДОБАВЛЯЮТСЯ к тем, что есть,
//	  а не затирают набор целиком;
//	· отсутствующее хранилище создаётся, но только типа dir — заводить
//	  LVM или ZFS автоматически слишком опасно, это отдельное решение;
//	· ничего не удаляется, и хранилища вне манифеста не трогаются вовсе.
type Storage struct{}

func (Storage) ID() string    { return "host/storage" }
func (Storage) Title() string { return "Хранилища" }

func (Storage) Describe() string {
	return `Добавляет хранилищам типы содержимого (content), описанные в манифесте, и
создаёт отсутствующие хранилища типа dir. Типы, которых нет в манифесте, но
есть на хосте, остаются на месте: ничего не удаляется. Хранилища, которых
нет в манифесте, не трогает.`
}

func (Storage) Configured(m *manifest.Manifest) bool { return len(m.Storages) > 0 }

func (s Storage) Plan(_ context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	var out plan.Changes

	for i, want := range m.Storages {
		if want.Name == "" {
			return out, fmt.Errorf("storages[%d]: не указано name", i)
		}
		resource := "хранилище " + want.Name
		wantContent := normalize(want.Content)

		have := f.Storage(want.Name)
		if have == nil {
			steps, note := s.planCreate(want, wantContent, resource)
			out.Steps = append(out.Steps, steps...)
			if note != nil {
				out.Notes = append(out.Notes, *note)
			}
			continue
		}

		missing := missingFrom(wantContent, have.Content)
		if len(missing) == 0 {
			continue
		}
		// Отдаём объединение, а не только недостающее: pvesm set заменяет
		// набор целиком, и передать одно лишь недостающее значило бы стереть
		// всё остальное.
		merged := union(wantContent, have.Content)
		out.Steps = append(out.Steps, plan.Step{
			ID:       fmt.Sprintf("%s:content:%s", s.ID(), want.Name),
			Provider: s.ID(),
			Resource: resource,
			Summary: fmt.Sprintf("добавить content: %s (было: %s, станет: %s)",
				strings.Join(missing, ","), orDash(strings.Join(have.Content, ",")), strings.Join(merged, ",")),
			Action: plan.ActionExec,
			Cmd:    []string{"pvesm", "set", want.Name, "--content", strings.Join(merged, ",")},
		})
	}
	return out, nil
}

func (s Storage) planCreate(want manifest.Storage, content []string, resource string) ([]plan.Step, *plan.Note) {
	if want.Type != "dir" || want.Path == "" {
		return nil, &plan.Note{
			Resource: resource,
			Message: fmt.Sprintf("хранилища «%s» нет; создать автоматически можно только type=dir с path — "+
				"опиши их в манифесте или заведи хранилище сам", want.Name),
		}
	}

	mkdirID := fmt.Sprintf("%s:mkdir:%s", s.ID(), want.Name)
	addID := fmt.Sprintf("%s:add:%s", s.ID(), want.Name)

	add := []string{"pvesm", "add", "dir", want.Name, "--path", want.Path}
	if len(content) > 0 {
		add = append(add, "--content", strings.Join(content, ","))
	}

	return []plan.Step{
		{
			ID:       mkdirID,
			Provider: s.ID(),
			Resource: resource,
			Summary:  "создать каталог " + want.Path,
			Action:   plan.ActionMkdir,
			Path:     want.Path,
		},
		{
			ID:       addID,
			Provider: s.ID(),
			Resource: resource,
			Summary: fmt.Sprintf("создать хранилище %s (dir, %s)%s", want.Name, want.Path,
				optional(", content="+strings.Join(content, ","), len(content) > 0)),
			Action: plan.ActionExec,
			Cmd:    add,
			// Заводить хранилище, каталог которого не создался, незачем:
			// pvesm его примет, а работать оно не будет.
			Needs: []string{mkdirID},
		},
	}, nil
}

func (s Storage) Verify(_ context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	var out []plan.Finding

	described := map[string]bool{}
	for _, want := range m.Storages {
		described[want.Name] = true
		resource := "хранилище " + want.Name
		wantContent := normalize(want.Content)

		have := f.Storage(want.Name)
		if have == nil {
			out = append(out, plan.Finding{Provider: s.ID(), Resource: resource,
				Message: "нет на хосте"})
			continue
		}
		if missing := missingFrom(wantContent, have.Content); len(missing) > 0 {
			out = append(out, plan.Finding{Provider: s.ID(), Resource: resource,
				Message: fmt.Sprintf("нет типов content: %s (есть: %s)",
					strings.Join(missing, ","), orDash(strings.Join(have.Content, ",")))})
			continue
		}
		out = append(out, plan.Finding{Provider: s.ID(), Resource: resource,
			Message: "в порядке", OK: true})
	}

	// Хранилище вне манифеста keel не тронет никогда — но расскажет о нём.
	for _, have := range f.Storages {
		if described[have.Name] {
			continue
		}
		out = append(out, plan.Finding{Provider: s.ID(), Resource: "хранилище " + have.Name,
			Message: "есть на хосте, но в манифесте не описано — keel его не трогает",
			OK:      true, Unmanaged: true})
	}
	return out, nil
}

// --- работа со списком content ----------------------------------------------

// normalize приводит список к виду, в котором его можно сравнивать:
// без пустых, без повторов, по алфавиту.
func normalize(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// missingFrom — то из want, чего нет в have.
func missingFrom(want, have []string) []string {
	present := map[string]bool{}
	for _, v := range have {
		present[v] = true
	}
	var out []string
	for _, v := range want {
		if !present[v] {
			out = append(out, v)
		}
	}
	return out
}

// union — то, что было, плюс то, что описано.
func union(want, have []string) []string {
	return normalize(append(append([]string(nil), want...), have...))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func optional(s string, when bool) string {
	if when {
		return s
	}
	return ""
}
