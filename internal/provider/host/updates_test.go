package host

import (
	"context"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

func updatesPlan(t *testing.T, body string, f *facts.Facts) plan.Changes {
	t.Helper()
	c, err := (Updates{}).Plan(context.Background(), parse(t, body), f)
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func TestUpdatesUsesDistUpgrade(t *testing.T) {
	f := &facts.Facts{Upgradable: 12, Upgradables: []string{"pve-manager", "proxmox-kernel-6.14"},
		UpgradeBytes: 340 << 20}
	c := updatesPlan(t, `{"host":{"updates":true}}`, f)

	got := commands(c.Steps)
	// dist-upgrade, а не upgrade: Proxmox иногда меняет состав пакетов,
	// и обычный upgrade такие переходы пропускает, оставляя систему
	// на полпути.
	want := []string{
		"apt-get update -o Acquire::Retries=1 -o Acquire::http::Timeout=30",
		"env DEBIAN_FRONTEND=noninteractive apt-get -y dist-upgrade -o Acquire::Retries=1 -o Acquire::http::Timeout=30",
	}
	assertCommands(t, got, want)

	// Установка ждёт обновления списка: ставить по старому списку — не то же
	// самое, что не ставить вовсе.
	if len(c.Steps[1].Needs) != 1 || c.Steps[1].Needs[0] != c.Steps[0].ID {
		t.Errorf("установка не ждёт обновления списка: %+v", c.Steps[1].Needs)
	}
}

// «12 пакетов» человеку говорит куда меньше, чем «12 пакетов, среди них
// ядро»: от состава зависит, нужна ли будет перезагрузка.
func TestUpdatesNamesPackages(t *testing.T) {
	f := &facts.Facts{Upgradable: 12, UpgradeBytes: 340 << 20,
		Upgradables: []string{"pve-manager", "proxmox-kernel-6.14", "libc6"}}
	c := updatesPlan(t, `{"host":{"updates":true}}`, f)

	summary := c.Steps[1].Summary
	for _, want := range []string{"12", "340 МБ", "pve-manager", "proxmox-kernel-6.14"} {
		if !strings.Contains(summary, want) {
			t.Errorf("в описании нет %q: %s", want, summary)
		}
	}
}

// Ради этого условие и появилось: однажды apt дорос до 6,4 ГБ и едва не
// увёл хост в OOM на сети, отдававшей 4 КБ/с.
func TestUpdatesGuardsAgainstSlowNetwork(t *testing.T) {
	f := &facts.Facts{Upgradable: 3, UpgradeURI: "http://download.proxmox.com/x.deb"}
	c := updatesPlan(t, `{"host":{"updates":true}}`, f)

	upgrade := c.Steps[1]
	if len(upgrade.Guards) != 1 || upgrade.Guards[0].Kind != plan.GuardNetSpeed {
		t.Fatalf("установка не защищена проверкой связи: %+v", upgrade.Guards)
	}
	// Порог по умолчанию — 30K, как и было.
	if upgrade.Guards[0].Arg != "30K" {
		t.Errorf("порог по умолчанию: %q", upgrade.Guards[0].Arg)
	}

	// Условие проверяется перед шагом, а не при сборке плана: между
	// просмотром и применением проходит время, и связь — как раз то,
	// что за это время меняется.
	if c.Steps[0].Guards != nil {
		t.Error("обновление списка пакетов тоже защищено — оно мало весит и нужно всегда")
	}
}

func TestUpdatesGuardCanBeTurnedOff(t *testing.T) {
	f := &facts.Facts{Upgradable: 3, UpgradeURI: "http://download.proxmox.com/x.deb"}
	c := updatesPlan(t, `{"host":{"updates":true,"updates_min_speed":"0"}}`, f)

	if len(c.Steps[1].Guards) != 0 {
		t.Errorf("проверка связи не выключилась ключом «0»: %+v", c.Steps[1].Guards)
	}
}

// apt иногда спрашивает, что делать с изменённым конфигом. В окне без
// терминала такой вопрос повисает молча и выглядит зависанием.
func TestUpdatesGivesTerminalToApt(t *testing.T) {
	c := updatesPlan(t, `{"host":{"updates":true}}`, &facts.Facts{Upgradable: 1})
	if !c.Steps[1].Interactive {
		t.Error("установка обновлений не помечена интерактивной")
	}
}

func TestUpdatesNothingToDo(t *testing.T) {
	if c := updatesPlan(t, `{"host":{"updates":true}}`, &facts.Facts{}); !c.Empty() {
		t.Errorf("обновлять нечего, но появились шаги: %+v", c.Steps)
	}
}

// При битых источниках apt-get -s ничего не печатает, и «ноль обновлений»
// становится неотличим от «всё свежее». Молчать об этом нельзя.
func TestUpdatesRefusesOnBrokenSources(t *testing.T) {
	f := &facts.Facts{AptError: "E: The repository 'https://enterprise.proxmox.com/debian/pve trixie Release' is not signed."}
	_, err := (Updates{}).Plan(context.Background(), parse(t, `{"host":{"updates":true}}`), f)
	if err == nil {
		t.Fatal("битые источники приняты за «обновлять нечего»")
	}
	if !strings.Contains(err.Error(), "host/repos") {
		t.Errorf("ошибка не подсказывает, чем чинить: %v", err)
	}
}

// Перезагрузку keel не делает и не предлагает — но и молчать о ней не должен.
func TestUpdatesMentionsPendingReboot(t *testing.T) {
	f := &facts.Facts{Upgradable: 1, RebootRequired: true}
	c := updatesPlan(t, `{"host":{"updates":true}}`, f)

	if len(c.Notes) != 1 || !strings.Contains(c.Notes[0].Message, "перезагруз") {
		t.Errorf("о запрошенной перезагрузке не сказано: %+v", c.Notes)
	}
	for _, cmd := range commands(c.Steps) {
		if strings.Contains(cmd, "reboot") {
			t.Errorf("keel собрался перезагружать хост: %s", cmd)
		}
	}
}

func TestUpdatesRuleOfZero(t *testing.T) {
	cases := map[string]bool{
		`{"host":{}}`:                false,
		`{"host":{"updates":false}}`: false,
		`{"host":{"updates":true}}`:  true,
	}
	for body, want := range cases {
		if got := (Updates{}).Configured(parse(t, body)); got != want {
			t.Errorf("%s: Configured = %v, ожидалось %v", body, got, want)
		}
	}
}

func TestSpeedToBytes(t *testing.T) {
	cases := map[string]int64{
		"30K": 30720, "2M": 2097152, "4096": 4096, "0": 0,
		"": 0, "мусор": 0, "30k": 30720, "1m": 1048576, "-5": 0,
	}
	for in, want := range cases {
		if got := SpeedToBytes(in); got != want {
			t.Errorf("SpeedToBytes(%q) = %d, ожидалось %d", in, got, want)
		}
	}
}
