package diff

import (
	"strings"
	"testing"
)

func TestIdenticalGivesEmpty(t *testing.T) {
	if got := Unified("одно и то же\n", "одно и то же\n", "было", "станет"); got != "" {
		t.Errorf("совпадающее содержимое дало разницу:\n%s", got)
	}
}

func TestShowsChangedLine(t *testing.T) {
	old := "Types: deb\nURIs: https://enterprise.proxmox.com/debian/pve\nSuites: trixie\n"
	new := "Types: deb\nURIs: http://download.proxmox.com/debian/pve\nSuites: trixie\n"

	got := Unified(old, new, "/etc/apt/sources.list.d/pve.sources (сейчас)",
		"/etc/apt/sources.list.d/pve.sources (станет)")

	for _, want := range []string{
		"--- /etc/apt/sources.list.d/pve.sources (сейчас)",
		"+++ /etc/apt/sources.list.d/pve.sources (станет)",
		"-URIs: https://enterprise.proxmox.com/debian/pve",
		"+URIs: http://download.proxmox.com/debian/pve",
		" Types: deb",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в разнице нет %q:\n%s", want, got)
		}
	}
}

func TestNewFileIsAllAdditions(t *testing.T) {
	got := Unified("", "первая\nвторая\n", "нет файла", "станет")
	if strings.Contains(got, "\n-") {
		t.Errorf("у нового файла появились удаления:\n%s", got)
	}
	if !strings.Contains(got, "+первая") || !strings.Contains(got, "+вторая") {
		t.Errorf("новые строки потеряны:\n%s", got)
	}
}

// Показывать надо изменение, а не весь файл: конфиг на триста строк
// целиком на экран не влезет, и человек перестанет их читать.
func TestLongFileShowsOnlyContext(t *testing.T) {
	var a, b []string
	for i := 0; i < 200; i++ {
		a = append(a, "строка")
		b = append(b, "строка")
	}
	b[100] = "изменённая строка"

	got := Unified(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", "было", "станет")

	lines := strings.Count(got, "\n")
	if lines > 15 {
		t.Errorf("на экран выведено %d строк вместо небольшого куска:\n%s", lines, got)
	}
	if !strings.Contains(got, "+изменённая строка") {
		t.Errorf("само изменение не показано:\n%s", got)
	}
	if !strings.Contains(got, "@@ -") {
		t.Errorf("нет заголовка куска:\n%s", got)
	}
}

// Две правки далеко друг от друга — это два куска, а не один на весь файл.
func TestDistantChangesGiveTwoHunks(t *testing.T) {
	var a []string
	for i := 0; i < 100; i++ {
		a = append(a, "строка")
	}
	b := append([]string(nil), a...)
	b[10] = "первая правка"
	b[90] = "вторая правка"

	got := Unified(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", "было", "станет")
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Errorf("кусков %d, ожидалось 2:\n%s", n, got)
	}
}

func TestDeletionIsShown(t *testing.T) {
	got := Unified("одна\nлишняя\nтри\n", "одна\nтри\n", "было", "станет")
	if !strings.Contains(got, "-лишняя") {
		t.Errorf("удаление не показано:\n%s", got)
	}
	if strings.Contains(got, "+лишняя") {
		t.Errorf("удаление показано как добавление:\n%s", got)
	}
}
