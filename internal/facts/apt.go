package facts

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AptSource — один файл источников apt, разобранный так же, как его читает
// сам apt: по содержимому, а не по имени.
//
// Это важнее, чем кажется. Тот же репозиторий, положенный другим
// инструментом под своим именем, при поиске по именам файлов невидим —
// и keel завёл бы вторую копию, а apt на два одинаковых источника
// отвечает руганью на каждом запуске.
type AptSource struct {
	Path string
	// Deb822 — формат *.sources (PVE 9 / Debian 13). Иначе классический *.list.
	Deb822 bool
	Raw    string
	// Components — всё, что включено в этом файле: pve-no-subscription,
	// pve-enterprise, pvetest, main…
	Components []string
	URIs       []string
	// Disabled — источник выключен целиком и apt его не читает.
	Disabled bool
}

// HasComponent — включён ли компонент в этом файле. Выключенный файл не
// считается: apt его не читает, значит и репозитория в нём нет.
func (s AptSource) HasComponent(name string) bool {
	if s.Disabled {
		return false
	}
	for _, c := range s.Components {
		if c == name {
			return true
		}
	}
	return false
}

// MentionsHost — упоминается ли хост среди URI. Платный ceph отвечает 401
// без подписки, и искать его надо по адресу, а не по имени файла.
func (s AptSource) MentionsHost(host string) bool {
	for _, u := range s.URIs {
		if strings.Contains(u, host) {
			return true
		}
	}
	return false
}

// AptSource находит источник по пути.
func (f *Facts) AptSource(path string) *AptSource {
	for i := range f.AptSources {
		if f.AptSources[i].Path == path {
			return &f.AptSources[i]
		}
	}
	return nil
}

// collectAptSources читает /etc/apt/sources.list и весь sources.list.d.
func (f *Facts) collectAptSources(sys func(string) string) {
	var files []string
	if _, err := os.Stat(sys("/etc/apt/sources.list")); err == nil {
		files = append(files, "/etc/apt/sources.list")
	}
	dir := sys("/etc/apt/sources.list.d")
	for _, pattern := range []string{"*.sources", "*.list"} {
		for _, full := range glob(filepath.Join(dir, pattern)) {
			files = append(files, filepath.Join("/etc/apt/sources.list.d", filepath.Base(full)))
		}
	}
	sort.Strings(files)

	for _, rel := range files {
		raw, err := os.ReadFile(sys(rel))
		if err != nil {
			continue
		}
		f.AptSources = append(f.AptSources, parseAptSource(rel, string(raw)))
	}
}

// ParseAptSource разбирает один файл источников. Открыт наружу, чтобы
// тесты провайдеров собирали подставной хост тем же разбором, каким keel
// читает настоящий, — иначе они проверяли бы выдуманное состояние.
func ParseAptSource(path, raw string) AptSource { return parseAptSource(path, raw) }

func parseAptSource(path, raw string) AptSource {
	s := AptSource{Path: path, Raw: raw, Deb822: strings.HasSuffix(path, ".sources")}
	if s.Deb822 {
		parseDeb822(&s)
		return s
	}
	parseList(&s)
	return s
}

// parseList разбирает классический формат: «deb URI SUITE КОМПОНЕНТЫ…».
// Выключенным считается файл, в котором не осталось ни одной живой строки
// deb — именно так выглядит закомментированный репозиторий.
func parseList(s *AptSource) {
	live := 0
	for _, line := range strings.Split(s.Raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || (fields[0] != "deb" && fields[0] != "deb-src") {
			continue
		}
		live++
		s.URIs = append(s.URIs, fields[1])
		s.Components = append(s.Components, fields[3:]...)
	}
	s.Disabled = live == 0
	s.Components = uniqueSorted(s.Components)
	s.URIs = uniqueSorted(s.URIs)
}

// parseDeb822 разбирает *.sources. Записи разделяются пустой строкой, и у
// каждой обязано быть поле Types.
//
// Выключенным файл считается только если записи есть и у каждой стоит
// выключатель. Запись без Types — это брак: apt на таком файле отказывается
// читать вообще все источники, поэтому она тоже считается «не выключено»,
// чтобы применение её переписало.
func parseDeb822(s *AptSource) {
	stanzas, broken := 0, false
	disabledAll := true

	for _, block := range splitStanzas(s.Raw) {
		hasTypes, off := false, false
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
			switch key {
			case "types":
				hasTypes = true
			case "uris":
				s.URIs = append(s.URIs, strings.Fields(val)...)
			case "components":
				s.Components = append(s.Components, strings.Fields(val)...)
			case "enabled":
				switch strings.ToLower(val) {
				case "false", "no", "0":
					off = true
				}
			}
		}
		if !hasTypes {
			// Пустой блок из одних комментариев записью не считается.
			if strings.TrimSpace(stripComments(block)) != "" {
				broken = true
			}
			continue
		}
		stanzas++
		if !off {
			disabledAll = false
		}
	}

	s.Disabled = stanzas > 0 && disabledAll && !broken
	s.Components = uniqueSorted(s.Components)
	s.URIs = uniqueSorted(s.URIs)
}

func splitStanzas(raw string) []string {
	var out []string
	var cur []string
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

func stripComments(block string) string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
