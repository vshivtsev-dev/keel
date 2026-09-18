// Package guests создаёт описанных в манифесте гостей.
//
// Железное правило, ради которого всё это и написано: существующий гость
// НЕ ТРОГАЕТСЯ. Совпал id — keel сообщает, чем гость отличается от
// описания, и проходит мимо. Ни перезаписи, ни «приведения в
// соответствие», ни удаления. Виртуалка с данными дороже любой стройности,
// и приводить её к описанию — решение человека, а не скрипта.
package guests

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/image"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/profile"
)

type Guests struct {
	// Images ищет живой адрес образа. Вынесен наружу, чтобы тесты не
	// ходили в сеть.
	Images *image.Resolver
	// Only сужает работу до перечисленных номеров: этим пользуется флаг
	// --guest и экраны выбора.
	Only map[int]bool
	// CacheDir — куда складываются скачанные образы.
	CacheDir string
}

func (Guests) ID() string { return "guests" }
func (Guests) Title() string {
	return "Гости: виртуальные машины и контейнеры"
}

func (Guests) Describe() string {
	return `Создаёт описанных в манифесте гостей: виртуальные машины из образов и
облачных образов, контейнеры LXC. Существующих гостей не изменяет и не
удаляет — только сообщает, чем они отличаются от описания.`
}

func (Guests) Configured(m *manifest.Manifest) bool { return len(m.Guests) > 0 }

func (g Guests) selected(id int) bool {
	return len(g.Only) == 0 || g.Only[id]
}

func (g Guests) Plan(ctx context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	var out plan.Changes
	skipped := 0

	for _, want := range m.Guests {
		if !g.selected(want.ID) {
			skipped++
			continue
		}
		p, err := loadProfile(want)
		if err != nil {
			return out, err
		}

		// Существующего гостя не трогаем. Но и молчать о расхождении
		// нельзя: человек описал одно, а на хосте другое.
		if f.GuestExists(want.ID) {
			out.Notes = append(out.Notes, plan.Note{
				Resource: guestName(want),
				Message:  "уже есть на хосте — keel его не трогает" + driftSuffix(want, p, f),
			})
			continue
		}

		steps, err := g.planGuest(ctx, want, p, f)
		if err != nil {
			return out, err
		}
		out.Steps = append(out.Steps, steps...)
	}

	// Молчать о сужении нельзя: иначе «уже в порядке» читается как
	// «все гости на месте», хотя половину мы даже не смотрели.
	if skipped > 0 {
		out.Notes = append(out.Notes, plan.Note{
			Resource: "выбор",
			Message: fmt.Sprintf("пропущено по выбору: %d — остальные гости из манифеста не рассматривались",
				skipped),
		})
	}
	return out, nil
}

func (g Guests) Verify(_ context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	var out []plan.Finding

	described := map[int]bool{}
	for _, want := range m.Guests {
		described[want.ID] = true
		if !g.selected(want.ID) {
			continue
		}
		p, err := loadProfile(want)
		if err != nil {
			return nil, err
		}
		if !f.GuestExists(want.ID) {
			out = append(out, plan.Finding{Provider: "guests", Resource: guestName(want),
				Message: "нет на хосте"})
			continue
		}
		msg := "на месте"
		if d := driftSuffix(want, p, f); d != "" {
			msg += d
		}
		out = append(out, plan.Finding{Provider: "guests", Resource: guestName(want),
			Message: msg, OK: true})
	}

	// Гость вне манифеста keel не тронет никогда — но расскажет о нём.
	for _, have := range f.Guests {
		if described[have.ID] {
			continue
		}
		kind := "ВМ"
		if have.Kind == "lxc" {
			kind = "контейнер"
		}
		out = append(out, plan.Finding{Provider: "guests",
			Resource: fmt.Sprintf("%d «%s»", have.ID, orDash(have.Name)),
			Message:  kind + " есть на хосте, но в манифесте не описан — keel его не трогает",
			OK:       true, Unmanaged: true})
	}
	return out, nil
}

// planGuest выбирает способ создания по виду из профиля. Второго
// источника правды о виде гостя нет.
func (g Guests) planGuest(ctx context.Context, want manifest.Guest, p *profile.Profile, f *facts.Facts) ([]plan.Step, error) {
	switch p.Kind {
	case profile.KindVMImage:
		return g.planVMFromImage(ctx, want, p, f)
	case profile.KindVMCloudInit:
		return g.planVMCloudInit(ctx, want, p, f)
	case profile.KindLXC:
		return g.planLXC(want, p, f)
	}
	return nil, fmt.Errorf("%s: непонятный вид гостя %q в профиле %s",
		guestName(want), p.Kind, p.Name)
}

func loadProfile(want manifest.Guest) (*profile.Profile, error) {
	name, err := profile.Resolve(want.Profile, want.Graphics)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", guestName(want), err)
	}
	if name == "" {
		return nil, fmt.Errorf("%s: не понял, какой профиль использовать (profile/graphics)", guestName(want))
	}
	p, err := profile.Load(name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", guestName(want), err)
	}
	return p, nil
}

func guestName(want manifest.Guest) string {
	return fmt.Sprintf("%d «%s»", want.ID, orDefault(want.Name, "без имени"))
}

// driftSuffix рассказывает, чем существующий гость отличается от
// описания. Только рассказывает: править его keel не станет.
func driftSuffix(want manifest.Guest, p *profile.Profile, f *facts.Facts) string {
	var parts []string
	if want.Cores != nil {
		if have := f.GuestField(want.ID, "cores"); have != "" && have != strconv.Itoa(*want.Cores) {
			parts = append(parts, fmt.Sprintf("ядер: на хосте %s, в манифесте %d", have, *want.Cores))
		}
	}
	if want.Memory != nil {
		if have := f.GuestField(want.ID, "memory"); have != "" && have != strconv.Itoa(*want.Memory) {
			parts = append(parts, fmt.Sprintf("память: на хосте %s, в манифесте %d", have, *want.Memory))
		}
	}
	_ = p
	if len(parts) == 0 {
		return ""
	}
	return ".\nОтличия от манифеста: " + strings.Join(parts, "; ")
}

// --- значения гостя ----------------------------------------------------------
//
// Сначала манифест, потом умолчание профиля, потом общее. Порядок важен:
// профиль знает, сколько памяти нужно рабочему столу, а манифест — сколько
// её на этой машине.

func cores(want manifest.Guest, p *profile.Profile) int {
	if want.Cores != nil {
		return *want.Cores
	}
	if p.Defaults.Cores != nil {
		return *p.Defaults.Cores
	}
	return 2
}

func memory(want manifest.Guest, p *profile.Profile) int {
	if want.Memory != nil {
		return *want.Memory
	}
	if p.Defaults.Memory != nil {
		return *p.Defaults.Memory
	}
	return 2048
}

func disk(want manifest.Guest, p *profile.Profile) string {
	return orDefault(want.Disk, p.Defaults.Disk)
}

func storage(want manifest.Guest) string { return orDefault(want.Storage, "local-lvm") }
func bridge(want manifest.Guest) string  { return orDefault(want.Bridge, "vmbr0") }
func onBoot(want manifest.Guest) bool    { return want.StartOnBoot != nil && *want.StartOnBoot }
func startNow(want manifest.Guest) bool  { return want.Start != nil && *want.Start }

// diskToGB: «64G» → 64, «65536M» → 64, «64» → 64.
func diskToGB(size string) int {
	if size == "" {
		return 0
	}
	num := strings.TrimRight(size, "GgMmTt")
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0
	}
	switch size[len(size)-1] {
	case 'M', 'm':
		return n / 1024
	case 'T', 't':
		return n * 1024
	}
	return n
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDash(v string) string { return orDefault(v, "—") }
