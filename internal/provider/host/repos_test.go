package host

import (
	"context"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

func pveHost(sources ...facts.AptSource) *facts.Facts {
	return &facts.Facts{
		Hostname:   "pve-01",
		Codename:   "trixie",
		RepoStyle:  "deb822",
		Keyring:    "/usr/share/keyrings/proxmox-archive-keyring.gpg",
		AptSources: sources,
	}
}

func source(path, raw string) facts.AptSource {
	return facts.ParseAptSource(path, raw)
}

const enterpriseRaw = `Types: deb
URIs: https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
`

func reposPlan(t *testing.T, repos string, f *facts.Facts) plan.Changes {
	t.Helper()
	m := parse(t, `{"host":{"repos":"`+repos+`"}}`)
	c, err := (Repos{}).Plan(context.Background(), m, f)
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func writeStep(t *testing.T, c plan.Changes, path string) plan.Step {
	t.Helper()
	for _, s := range c.Steps {
		if s.Action == plan.ActionWrite && s.Path == path {
			return s
		}
	}
	t.Fatalf("нет шага записи в %s; есть: %+v", path, c.Steps)
	return plan.Step{}
}

// Свежеустановленный PVE смотрит в платный репозиторий и ругается на
// каждом apt update. Это главный случай, ради которого модуль существует.
func TestReposSwitchesFromEnterprise(t *testing.T) {
	f := pveHost(source("/etc/apt/sources.list.d/pve-enterprise.sources", enterpriseRaw))
	c := reposPlan(t, repoNoSub, f)

	enable := writeStep(t, c, "/etc/apt/sources.list.d/pve-no-subscription.sources")
	body := string(enable.Content)
	for _, want := range []string{
		"Types: deb",
		"URIs: http://download.proxmox.com/debian/pve",
		"Suites: trixie",
		"Components: pve-no-subscription",
		"Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в новом файле нет %q:\n%s", want, body)
		}
	}

	disable := writeStep(t, c, "/etc/apt/sources.list.d/pve-enterprise.sources")
	if !strings.Contains(string(disable.Content), "Enabled: false") {
		t.Errorf("платный репозиторий не выключен:\n%s", disable.Content)
	}

	// Список пакетов после смены надо перечитать — иначе apt будет ходить
	// по старому.
	if got := commands(c.Steps); len(got) != 1 || !strings.HasPrefix(got[0], "apt-get update") {
		t.Errorf("не обновляется список пакетов: %v", got)
	}
}

// Соблазн дописать выключатель в конец файла велик и ошибочен: получилась
// бы запись без Types, а на таком файле apt отказывается читать вообще все
// источники — не только этот.
func TestReposDisablesInsideStanza(t *testing.T) {
	f := pveHost(source("/etc/apt/sources.list.d/pve-enterprise.sources", enterpriseRaw))
	c := reposPlan(t, repoNoSub, f)
	body := string(writeStep(t, c, "/etc/apt/sources.list.d/pve-enterprise.sources").Content)

	// Выключатель обязан стоять внутри записи, до разделяющей пустой строки.
	stanzas := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(stanzas) != 1 {
		t.Fatalf("появилась лишняя запись — apt перестанет читать источники:\n%s", body)
	}
	if !strings.Contains(stanzas[0], "Types: deb") || !strings.Contains(stanzas[0], "Enabled: false") {
		t.Errorf("выключатель не внутри записи:\n%s", body)
	}

	// И повторный прогон по своему же результату ничего не добавляет.
	again := pveHost(source("/etc/apt/sources.list.d/pve-enterprise.sources", body))
	if steps := reposPlan(t, repoNoSub, again).Steps; len(steps) != 1 {
		for _, s := range steps {
			if s.Path == "/etc/apt/sources.list.d/pve-enterprise.sources" {
				t.Errorf("уже выключенный репозиторий выключается снова:\n%s", s.Content)
			}
		}
	}
}

// Главная причина читать содержимое, а не имена файлов: репозиторий,
// положенный другим инструментом, keel не должен заводить второй раз —
// apt на два одинаковых источника отвечает руганью.
func TestReposDoesNotDuplicateRepoEnabledElsewhere(t *testing.T) {
	other := strings.Replace(enterpriseRaw, "https://enterprise.proxmox.com/debian/pve",
		"http://download.proxmox.com/debian/pve", 1)
	other = strings.Replace(other, "pve-enterprise", "pve-no-subscription", 1)

	f := pveHost(source("/etc/apt/sources.list.d/мой-набор.sources", other))
	c := reposPlan(t, repoNoSub, f)

	for _, s := range c.Steps {
		if s.Action == plan.ActionWrite && strings.Contains(s.Path, "pve-no-subscription") {
			t.Errorf("заведена вторая копия того же репозитория: %s", s.Path)
		}
	}
	if len(c.Notes) == 0 || !strings.Contains(c.Notes[0].Message, "мой-набор") {
		t.Errorf("о том, где репозиторий уже включён, не сказано: %+v", c.Notes)
	}
}

// Платный ceph лежит в своём файле и без подписки отвечает 401.
func TestReposDisablesEnterpriseCephByURI(t *testing.T) {
	ceph := `Types: deb
URIs: https://enterprise.proxmox.com/debian/ceph-squid
Suites: trixie
Components: enterprise
`
	f := pveHost(
		source("/etc/apt/sources.list.d/pve-enterprise.sources", enterpriseRaw),
		source("/etc/apt/sources.list.d/ceph.sources", ceph),
	)
	c := reposPlan(t, repoNoSub, f)

	body := string(writeStep(t, c, "/etc/apt/sources.list.d/ceph.sources").Content)
	if !strings.Contains(body, "Enabled: false") {
		t.Errorf("платный ceph не выключен:\n%s", body)
	}
}

// А при переходе НА enterprise его трогать не надо.
func TestReposKeepsCephWhenTargetIsEnterprise(t *testing.T) {
	ceph := "Types: deb\nURIs: https://enterprise.proxmox.com/debian/ceph-squid\nSuites: trixie\nComponents: enterprise\n"
	f := pveHost(source("/etc/apt/sources.list.d/ceph.sources", ceph))
	c := reposPlan(t, repoEnterprise, f)

	for _, s := range c.Steps {
		if s.Path == "/etc/apt/sources.list.d/ceph.sources" {
			t.Error("платный ceph выключен при переходе на платный репозиторий")
		}
	}
}

// Чужие репозитории — не наше дело.
func TestReposNeverTouchesUnrelatedSources(t *testing.T) {
	debian := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main contrib\n"
	f := pveHost(
		source("/etc/apt/sources.list.d/debian.sources", debian),
		source("/etc/apt/sources.list.d/pve-enterprise.sources", enterpriseRaw),
	)
	c := reposPlan(t, repoNoSub, f)

	for _, s := range c.Steps {
		if s.Path == "/etc/apt/sources.list.d/debian.sources" {
			t.Error("тронут посторонний репозиторий Debian")
		}
	}
}

func TestReposClassicListFormat(t *testing.T) {
	f := pveHost()
	f.RepoStyle = "list"
	f.Codename = "bookworm"
	f.AptSources = []facts.AptSource{source("/etc/apt/sources.list.d/pve-enterprise.list",
		"deb https://enterprise.proxmox.com/debian/pve bookworm pve-enterprise\n")}

	c := reposPlan(t, repoNoSub, f)

	enable := writeStep(t, c, "/etc/apt/sources.list.d/pve-no-subscription.list")
	if !strings.Contains(string(enable.Content),
		"deb http://download.proxmox.com/debian/pve bookworm pve-no-subscription") {
		t.Errorf("строка репозитория собрана неверно:\n%s", enable.Content)
	}

	disable := writeStep(t, c, "/etc/apt/sources.list.d/pve-enterprise.list")
	if !strings.Contains(string(disable.Content), "# Выключено keel: deb https://enterprise") {
		t.Errorf("репозиторий не закомментирован:\n%s", disable.Content)
	}
}

func TestReposNothingToDoWhenAlreadyRight(t *testing.T) {
	f := pveHost()
	f.AptSources = []facts.AptSource{
		source("/etc/apt/sources.list.d/pve-no-subscription.sources", repoContent(repoNoSub, f)),
	}
	if c := reposPlan(t, repoNoSub, f); !c.Empty() {
		t.Errorf("настроенный хост вызвал изменения: %+v", c.Steps)
	}
}

func TestReposRejectsUnknownValue(t *testing.T) {
	m := parse(t, `{"host":{"repos":"бесплатный"}}`)
	_, err := (Repos{}).Plan(context.Background(), m, pveHost())
	if err == nil {
		t.Fatal("неизвестное значение host.repos принято")
	}
	if !strings.Contains(err.Error(), "no-subscription") {
		t.Errorf("ошибка не подсказывает допустимые значения: %v", err)
	}
}

func TestReposRuleOfZero(t *testing.T) {
	if (Repos{}).Configured(parse(t, `{"host":{}}`)) {
		t.Error("без ключа host.repos провайдер объявил себя настроенным")
	}
}
