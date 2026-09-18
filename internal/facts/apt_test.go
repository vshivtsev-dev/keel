package facts

import "testing"

// Формат PVE 9 / Debian 13.
const enterpriseSources = `Types: deb
URIs: https://enterprise.proxmox.com/debian/pve
Suites: trixie
Components: pve-enterprise
Signed-By: /usr/share/keyrings/proxmox-archive-keyring.gpg
`

func TestParseDeb822ReadsComponents(t *testing.T) {
	s := parseAptSource("/etc/apt/sources.list.d/pve-enterprise.sources", enterpriseSources)

	if !s.Deb822 {
		t.Error("формат определён неверно")
	}
	if !s.HasComponent("pve-enterprise") {
		t.Errorf("компонент не найден: %v", s.Components)
	}
	if s.Disabled {
		t.Error("включённый источник объявлен выключенным")
	}
	if !s.MentionsHost("enterprise.proxmox.com") {
		t.Errorf("адрес не найден: %v", s.URIs)
	}
}

func TestParseDeb822SeesDisabled(t *testing.T) {
	s := parseAptSource("/etc/apt/sources.list.d/pve-enterprise.sources",
		enterpriseSources+"# Выключено keel\nEnabled: false\n")

	if !s.Disabled {
		t.Error("выключенный источник не распознан")
	}
	// Выключенный источник apt не читает — значит репозитория в нём нет.
	if s.HasComponent("pve-enterprise") {
		t.Error("компонент выключенного источника считается включённым")
	}
}

// Запись без Types — это брак, на котором apt отказывается читать вообще
// все источники. Такой файл выключенным считаться не должен: применение
// обязано его переписать.
func TestParseDeb822TreatsStanzaWithoutTypesAsBroken(t *testing.T) {
	broken := enterpriseSources + "\n# Выключено keel\nEnabled: false\n"
	s := parseAptSource("/etc/apt/sources.list.d/pve-enterprise.sources", broken)
	if s.Disabled {
		t.Error("битый файл объявлен исправно выключенным — его не перепишут")
	}
}

// Два репозитория в одном файле: пока жива хоть одна запись, файл включён.
func TestParseDeb822HandlesSeveralStanzas(t *testing.T) {
	two := enterpriseSources + "\n" + `Types: deb
URIs: http://download.proxmox.com/debian/pve
Suites: trixie
Components: pve-no-subscription
# Выключено keel
Enabled: false
`
	s := parseAptSource("/etc/apt/sources.list.d/both.sources", two)
	if s.Disabled {
		t.Error("файл с одной живой записью объявлен выключенным")
	}
	if !s.HasComponent("pve-enterprise") {
		t.Errorf("компоненты разобраны неверно: %v", s.Components)
	}
}

func TestParseListFormat(t *testing.T) {
	s := parseAptSource("/etc/apt/sources.list.d/pve-no-subscription.list",
		"# Создано keel\ndeb http://download.proxmox.com/debian/pve bookworm pve-no-subscription\n")

	if s.Deb822 {
		t.Error("классический формат принят за deb822")
	}
	if !s.HasComponent("pve-no-subscription") {
		t.Errorf("компонент не найден: %v", s.Components)
	}
	if s.Disabled {
		t.Error("включённый источник объявлен выключенным")
	}
}

func TestParseListSeesCommentedOut(t *testing.T) {
	s := parseAptSource("/etc/apt/sources.list.d/pve-enterprise.list",
		"# Выключено keel: deb https://enterprise.proxmox.com/debian/pve bookworm pve-enterprise\n")
	if !s.Disabled {
		t.Error("закомментированный источник не распознан как выключенный")
	}
	if s.HasComponent("pve-enterprise") {
		t.Error("компонент выключенного источника считается включённым")
	}
}

// Главная причина читать содержимое, а не имена: тот же репозиторий может
// лежать под любым именем, положенный другим инструментом.
func TestComponentFoundUnderAnyFileName(t *testing.T) {
	s := parseAptSource("/etc/apt/sources.list.d/мой-набор.sources", enterpriseSources)
	if !s.HasComponent("pve-enterprise") {
		t.Error("репозиторий под чужим именем не найден — keel завёл бы вторую копию")
	}
}
