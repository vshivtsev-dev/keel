package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Repos переключает apt между репозиториями Proxmox.
//
// Порт modules/host/10-repos.sh. Свежеустановленный PVE смотрит в платный
// enterprise-репозиторий; без подписки тот отвечает 401, и apt update
// ругается при каждом запуске.
//
// Выключенные репозитории не удаляются, а помечаются выключенными: вернуть
// обратно можно одной правкой, и видно, что именно keel сделал.
type Repos struct{}

const (
	repoNoSub      = "no-subscription"
	repoEnterprise = "enterprise"
	repoTest       = "test"
)

func (Repos) ID() string    { return "host/repos" }
func (Repos) Title() string { return "Репозитории Proxmox" }

func (Repos) Describe() string {
	return `Переключает apt между репозиториями Proxmox: бесплатным (no-subscription),
платным (enterprise) и тестовым (test). Выключенные репозитории не
удаляются, а помечаются выключенными — вернуть обратно можно одной правкой.`
}

func (Repos) Configured(m *manifest.Manifest) bool { return m.Host.Repos != "" }

func (r Repos) Plan(_ context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	target := m.Host.Repos
	if err := validRepo(target); err != nil {
		return plan.Changes{}, err
	}

	var out plan.Changes
	changed := false

	// Тот же репозиторий под чужим именем — не повод заводить свой: apt
	// читает все файлы, и две копии одного источника он встретит руганью.
	own := repoFile(target, f.RepoStyle)
	elsewhere := enabledElsewhere(f, target, own)

	switch {
	case len(elsewhere) > 0:
		for _, path := range elsewhere {
			out.Notes = append(out.Notes, plan.Note{
				Resource: "репозиторий " + target,
				Message:  "уже включён в " + path + " — свой файл не создаю",
			})
		}
		if src := f.AptSource(own); src != nil && !src.Disabled {
			out.Notes = append(out.Notes, plan.Note{
				Resource: "репозиторий " + target,
				Message: "он же включён в " + own +
					": два одинаковых источника, apt будет ругаться. Лишний убери руками",
			})
		}
	default:
		want := repoContent(target, f)
		if src := f.AptSource(own); src == nil || src.Raw != want {
			out.Steps = append(out.Steps, plan.Step{
				ID:       r.ID() + ":enable",
				Provider: r.ID(),
				Resource: "репозиторий " + target,
				Summary:  fmt.Sprintf("включить репозиторий %s → %s", target, own),
				Action:   plan.ActionWrite,
				Path:     own,
				Content:  []byte(want),
			})
			changed = true
		}
	}

	for _, src := range othersToDisable(f, target) {
		out.Steps = append(out.Steps, plan.Step{
			ID:       r.ID() + ":disable:" + src.Path,
			Provider: r.ID(),
			Resource: "репозиторий " + src.Path,
			Summary:  "выключить: " + src.Path,
			Action:   plan.ActionWrite,
			Path:     src.Path,
			Content:  []byte(disabledContent(src)),
		})
		changed = true
	}

	if changed {
		// Ограничители те же, что у обновлений: не ждать две минуты
		// каждого молчащего зеркала и не копить очередь на повторах.
		out.Steps = append(out.Steps, plan.Step{
			ID:       r.ID() + ":update",
			Provider: r.ID(),
			Resource: "список пакетов",
			Summary:  "обновить список пакетов (apt update)",
			Action:   plan.ActionExec,
			Cmd: []string{"apt-get", "update",
				"-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=30"},
		})
	}
	return out, nil
}

func (r Repos) Verify(_ context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	target := m.Host.Repos
	if err := validRepo(target); err != nil {
		return nil, err
	}

	var out []plan.Finding
	resource := "репозиторий " + target
	own := repoFile(target, f.RepoStyle)

	switch elsewhere := enabledElsewhere(f, target, own); {
	case len(elsewhere) > 0:
		out = append(out, plan.Finding{Provider: r.ID(), Resource: resource,
			Message: "включён (в " + elsewhere[0] + ")", OK: true})
	default:
		src := f.AptSource(own)
		if src != nil && src.Raw == repoContent(target, f) {
			out = append(out, plan.Finding{Provider: r.ID(), Resource: resource,
				Message: "включён", OK: true})
		} else {
			out = append(out, plan.Finding{Provider: r.ID(), Resource: resource,
				Message: "не настроен (" + own + ")"})
		}
	}

	for _, src := range othersToDisable(f, target) {
		out = append(out, plan.Finding{Provider: r.ID(), Resource: "репозиторий " + src.Path,
			Message: "всё ещё включён"})
	}
	return out, nil
}

// --- какой файл и что в нём -------------------------------------------------

func validRepo(target string) error {
	switch target {
	case repoNoSub, repoEnterprise, repoTest:
		return nil
	}
	return fmt.Errorf("host.repos = «%s» — допустимо: %s, %s, %s",
		target, repoNoSub, repoEnterprise, repoTest)
}

func repoFile(target, style string) string {
	ext := ".list"
	if style == "deb822" {
		ext = ".sources"
	}
	return "/etc/apt/sources.list.d/pve-" + target + ext
}

func repoURI(target string) string {
	if target == repoEnterprise {
		return "https://enterprise.proxmox.com/debian/pve"
	}
	return "http://download.proxmox.com/debian/pve"
}

func repoComponent(target string) string {
	switch target {
	case repoNoSub:
		return "pve-no-subscription"
	case repoEnterprise:
		return "pve-enterprise"
	case repoTest:
		return "pvetest"
	}
	return ""
}

func repoContent(target string, f *facts.Facts) string {
	codename := f.Codename
	if codename == "" {
		codename = "bookworm"
	}
	if f.RepoStyle == "deb822" {
		return fmt.Sprintf(`# Создано keel. Репозиторий Proxmox VE: %s
Types: deb
URIs: %s
Suites: %s
Components: %s
Signed-By: %s
`, target, repoURI(target), codename, repoComponent(target), f.Keyring)
	}
	return fmt.Sprintf("# Создано keel. Репозиторий Proxmox VE: %s\ndeb %s %s %s\n",
		target, repoURI(target), codename, repoComponent(target))
}

// enabledElsewhere — файлы, где нужный репозиторий уже включён, кроме
// нашего собственного.
func enabledElsewhere(f *facts.Facts, target, own string) []string {
	comp := repoComponent(target)
	var out []string
	for _, src := range f.AptSources {
		if src.Path == own || !src.HasComponent(comp) {
			continue
		}
		out = append(out, src.Path)
	}
	return out
}

// othersToDisable — включённые источники, которые надо выключить при смене.
//
// Файл, в котором лежит нужный нам репозиторий, не выключается, даже если
// в нём заодно описан и чужой: один файл может нести несколько записей, и
// выключить его целиком значило бы отрубить и нужное.
func othersToDisable(f *facts.Facts, target string) []facts.AptSource {
	keep := repoComponent(target)

	var out []facts.AptSource
	for _, src := range f.AptSources {
		if src.Disabled || src.HasComponent(keep) {
			continue
		}

		hostile := false
		for _, repo := range []string{repoNoSub, repoEnterprise, repoTest} {
			if repo != target && src.HasComponent(repoComponent(repo)) {
				hostile = true
				break
			}
		}
		// Платный ceph отвечает 401 без подписки — ищем его по адресу,
		// а не по имени файла: он лежит в своём ceph.sources.
		if !hostile && target != repoEnterprise && src.MentionsHost("enterprise.proxmox.com") {
			hostile = true
		}
		if hostile {
			out = append(out, src)
		}
	}
	return out
}

// --- как выглядит выключенный источник ---------------------------------------

func disabledContent(src facts.AptSource) string {
	if src.Deb822 {
		return disableDeb822(src.Raw)
	}
	return disableList(src.Raw)
}

func disableList(raw string) string {
	var out []string
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "deb ") || strings.HasPrefix(trimmed, "deb-src ") {
			out = append(out, "# Выключено keel: "+line)
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// disableDeb822 дописывает выключатель ВНУТРЬ каждой записи.
//
// Соблазн дописать «Enabled: false» в конец файла через пустую строку
// велик и ошибочен: это создаёт вторую запись без поля Types, а на таком
// файле apt отказывается читать вообще все источники — не только этот.
// Проверено на живом хосте, поэтому оговорка здесь и остаётся.
//
// Повторный прогон по своему же результату ничего не меняет: старые
// служебные строки keel и любые Enabled сначала убираются.
func disableDeb822(raw string) string {
	var out []string
	for _, block := range splitBlocks(raw) {
		var kept []string
		hasTypes := false
		for _, line := range strings.Split(block, "\n") {
			trimmed := strings.ToLower(strings.TrimSpace(line))
			if trimmed == "# выключено keel" || strings.HasPrefix(trimmed, "enabled:") {
				continue
			}
			if strings.HasPrefix(trimmed, "types:") {
				hasTypes = true
			}
			kept = append(kept, line)
		}
		if len(kept) == 0 {
			continue
		}
		if hasTypes {
			kept = append(kept, "# Выключено keel", "Enabled: false")
		}
		out = append(out, strings.Join(kept, "\n"))
	}
	return strings.Join(out, "\n\n") + "\n"
}

func splitBlocks(raw string) []string {
	var out, cur []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}
