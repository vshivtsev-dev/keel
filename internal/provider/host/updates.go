package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Updates ставит обновления хоста.
//
// Порт modules/host/20-updates.sh. Обновление идёт через dist-upgrade,
// потому что Proxmox иногда меняет состав пакетов (ядро, зависимости), и
// обычный upgrade такие переходы пропускает, оставляя систему на полпути.
//
// Перезагрузку keel не делает и не предлагает — это решение остаётся за
// человеком.
type Updates struct{}

func (Updates) ID() string    { return "host/updates" }
func (Updates) Title() string { return "Обновление пакетов" }

func (Updates) Describe() string {
	return `Обновляет список пакетов и ставит доступные обновления (dist-upgrade).
Перед установкой показывает, что именно будет обновлено. Перезагрузку
не делает и не предлагает — это решение остаётся за тобой.`
}

func (Updates) Configured(m *manifest.Manifest) bool { return m.Host.UpdatesEnabled() }

// aptOpts — ограничители против того, чтобы apt утащил за собой хост.
// По умолчанию он повторяет неудачную закачку трижды и ждёт ответа две
// минуты: на сети, которая отвечает через раз, это максимум ожидания при
// минимуме результата — и растущая очередь закачек, которая и съедает
// память.
var aptOpts = []string{"-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=30"}

func (u Updates) Plan(_ context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	if f.AptError != "" {
		return plan.Changes{}, fmt.Errorf("apt не может прочитать списки источников:\n%s\n"+
			"Сначала почини репозитории: keel apply --only host/repos", indent(f.AptError))
	}

	var out plan.Changes
	if f.Upgradable == 0 {
		return out, nil
	}

	updateID := u.ID() + ":update"
	out.Steps = append(out.Steps, plan.Step{
		ID:       updateID,
		Provider: u.ID(),
		Resource: "список пакетов",
		Summary:  "обновить список пакетов",
		Action:   plan.ActionExec,
		Cmd:      append([]string{"apt-get", "update"}, aptOpts...),
	})

	upgrade := plan.Step{
		ID:       u.ID() + ":upgrade",
		Provider: u.ID(),
		Resource: "пакеты",
		Summary:  summarizeUpgrade(f),
		Action:   plan.ActionExec,
		Cmd: append([]string{"env", "DEBIAN_FRONTEND=noninteractive",
			"apt-get", "-y", "dist-upgrade"}, aptOpts...),
		// apt иногда спрашивает, что делать с изменённым конфигом. В окне
		// без терминала такой вопрос повисает молча и выглядит зависанием.
		Interactive: true,
		Needs:       []string{updateID},
		// Точный состав закачки решает apt в момент выполнения: список
		// пакетов к тому времени уже обновится.
		Unknown: []string{"итоговый список пакетов и объём закачки"},
	}

	if min := m.Host.UpdatesMinSpeedOr(); speedToBytes(min) > 0 {
		upgrade.Guards = []plan.Guard{{
			Kind: plan.GuardNetSpeed,
			Arg:  min,
			Why:  "связь до репозитория не медленнее " + min + "/с",
		}}
	}
	out.Steps = append(out.Steps, upgrade)

	if f.RebootRequired {
		out.Notes = append(out.Notes, plan.Note{
			Resource: "хост",
			Message:  "система уже просит перезагрузку — keel её не делает, перезагрузи сам, когда удобно",
		})
	}
	return out, nil
}

func (u Updates) Verify(_ context.Context, _ *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	if f.AptError != "" {
		return nil, fmt.Errorf("apt не может прочитать списки источников:\n%s", indent(f.AptError))
	}
	if f.Upgradable == 0 {
		return []plan.Finding{{Provider: u.ID(), Resource: "пакеты",
			Message: "все пакеты обновлены", OK: true}}, nil
	}
	return []plan.Finding{{Provider: u.ID(), Resource: "пакеты",
		Message: fmt.Sprintf("осталось необновлённых: %d", f.Upgradable)}}, nil
}

// summarizeUpgrade называет первые пакеты поимённо: «12 пакетов» человеку
// говорит куда меньше, чем «12 пакетов, среди них pve-manager и ядро».
func summarizeUpgrade(f *facts.Facts) string {
	const show = 6
	var b strings.Builder
	fmt.Fprintf(&b, "установить обновления: %d", f.Upgradable)
	if f.UpgradeBytes > 0 {
		fmt.Fprintf(&b, ", %s", humanBytes(f.UpgradeBytes))
	}
	if len(f.Upgradables) > 0 {
		names := f.Upgradables
		suffix := ""
		if len(names) > show {
			names, suffix = names[:show], fmt.Sprintf(" и ещё %d", len(f.Upgradables)-show)
		}
		fmt.Fprintf(&b, " (%s%s)", strings.Join(names, ", "), suffix)
	}
	return b.String()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " ГБ"
	case n >= 1<<20:
		return strconv.FormatInt(n/(1<<20), 10) + " МБ"
	case n >= 1<<10:
		return strconv.FormatInt(n/(1<<10), 10) + " КБ"
	default:
		return strconv.FormatInt(n, 10) + " Б"
	}
}

// SpeedToBytes: «30K» → 30720, «2M» → 2097152, «4096» → 4096. Мусор → 0.
// Открыт наружу: тем же разбором пользуется проверка связи перед закачкой.
func SpeedToBytes(v string) int64 { return speedToBytes(v) }

func speedToBytes(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	mult := int64(1)
	switch v[len(v)-1] {
	case 'K', 'k':
		mult, v = 1024, v[:len(v)-1]
	case 'M', 'm':
		mult, v = 1024*1024, v[:len(v)-1]
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n * mult
}

func indent(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, "  "+line)
	}
	return strings.Join(out, "\n")
}
