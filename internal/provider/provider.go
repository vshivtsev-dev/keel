// Package provider — области хоста, которые keel умеет приводить в порядок.
//
// Провайдер пришёл на смену модулю из bash-версии и отличается от него
// одним, но решающим свойством: он ничего не выполняет. Раньше модуль
// сначала печатал в mod_check, что собирается сделать, а потом делал это
// в mod_apply — те же ветвления, написанные дважды, и ни одной гарантии,
// что второй раз получится то же самое. Теперь провайдер один раз строит
// список шагов, и этот список показывается, сохраняется и исполняется.
//
// Провайдер не смеет трогать систему сам. За этим следит guard_test.go:
// он разбирает исходники пакета и падает, если сюда пробрался os/exec или
// запись в файл. Это прямой наследник tests/lint-run-guard.sh.
package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

type Provider interface {
	// ID вида "host/storage" — устойчивый, попадает в план и в --only.
	ID() string
	// Title — одна строка для дерева на экране.
	Title() string
	// Describe — что провайдер делает и чего не делает, человеческим языком.
	Describe() string

	// Plan возвращает изменения, которые приведут хост к описанному
	// состоянию, и заметки о том, что keel сделать не может.
	//
	// Пустой результат без ошибки значит «менять нечего». Правило нуля —
	// это другое, и отвечает за него Configured.
	Plan(ctx context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error)

	// Configured сообщает, описана ли эта область в манифесте вообще.
	// Ложь — правило нуля: провайдер не сделает ничего, и это норма,
	// а не ошибка и не «уже в порядке».
	Configured(m *manifest.Manifest) bool

	// Verify проверяет состояние постфактум, ничего не меняя.
	Verify(ctx context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error)
}

// Registry — набор провайдеров в порядке применения. Порядок важен:
// хранилища заводятся раньше гостей, которые на них лягут.
type Registry struct {
	list []Provider
}

func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{}
	for _, p := range ps {
		r.Add(p)
	}
	return r
}

func (r *Registry) Add(p Provider) {
	if r.Find(p.ID()) != nil {
		panic(fmt.Sprintf("провайдер %s уже зарегистрирован", p.ID()))
	}
	r.list = append(r.list, p)
}

func (r *Registry) All() []Provider { return r.list }

func (r *Registry) Find(id string) Provider {
	for _, p := range r.list {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// IDs возвращает отсортированный список идентификаторов — для справки и
// сообщений об ошибках, где порядок применения значения не имеет.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.list))
	for _, p := range r.list {
		out = append(out, p.ID())
	}
	sort.Strings(out)
	return out
}

// Only сужает набор до одного провайдера. Пустая строка — весь набор.
func (r *Registry) Only(id string) (*Registry, error) {
	if id == "" {
		return r, nil
	}
	p := r.Find(id)
	if p == nil {
		return nil, fmt.Errorf("нет провайдера %q; есть: %v", id, r.IDs())
	}
	return NewRegistry(p), nil
}
