package profile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..")
}

// Профили лежат в двух местах: profiles/ читает bash-версия, а
// internal/profile/ вкомпилирован в бинарник — go:embed не умеет брать
// файлы вне своего каталога.
//
// Две копии одного файла расходятся всегда, вопрос только когда. Пока
// обе версии живут рядом, за этим следит тест; на этапе 8, когда bash
// уйдёт, копия останется одна.
func TestEmbeddedProfilesMatchRepoCopies(t *testing.T) {
	root := repoRoot(t)
	names := Names()
	if len(names) == 0 {
		t.Fatal("во встроенных профилях пусто")
	}

	for _, name := range names {
		embedded, err := builtin.ReadFile(name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		onDisk, err := os.ReadFile(filepath.Join(root, "profiles", name+".json"))
		if err != nil {
			t.Errorf("профиля %s нет в profiles/: %v", name, err)
			continue
		}
		if string(embedded) != string(onDisk) {
			t.Errorf("профиль %s разошёлся: встроенная копия не совпадает с profiles/%s.json", name, name)
		}
	}

	// И наоборот: профиль, добавленный в profiles/, должен попасть внутрь.
	got, err := filepath.Glob(filepath.Join(root, "profiles", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(names) {
		t.Errorf("в profiles/ %d файлов, встроено %d — копии разошлись", len(got), len(names))
	}
}

// Каждый профиль обязан читаться и объявлять понятный вид гостя: из вида
// следует, чем гость создаётся, и второго источника правды об этом нет.
func TestEveryProfileLoads(t *testing.T) {
	for _, name := range Names() {
		p, err := Load(name)
		if err != nil {
			t.Errorf("профиль %s не читается: %v", name, err)
			continue
		}
		switch p.Kind {
		case KindVMImage, KindVMCloudInit, KindLXC:
		default:
			t.Errorf("профиль %s: непонятный kind %q", name, p.Kind)
		}
		if p.Title == "" {
			t.Errorf("профиль %s без заголовка — его нечем показать в списке", name)
		}

		switch p.Kind {
		case KindLXC:
			if p.Template == nil || p.Template.Pattern == "" {
				t.Errorf("профиль %s: контейнеру нужен шаблон", name)
			}
		default:
			if p.Image == nil || (p.Image.URL == "" && p.Image.URLTemplate == "" && p.Image.GitHubRepo == "") {
				t.Errorf("профиль %s: машине нужен образ", name)
			}
		}
	}
}

// Профиль с комментариями должен читаться: их там много, и они полезнее
// отдельного документа.
func TestProfileReadsComments(t *testing.T) {
	p, err := Load("cloudflared")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindLXC || !p.NeedsToken || p.TokenFile != "cloudflared" {
		t.Errorf("профиль разобран неверно: %+v", p)
	}
	if len(p.Runcmd) != 3 {
		t.Errorf("runcmd разобран неверно: %v", p.Runcmd)
	}
}

// «desktop» — это роль, а не реализация.
func TestResolveDesktopRole(t *testing.T) {
	cases := map[string]string{
		"dri":         "desktop-lxc",
		"virgl":       "desktop-vm",
		"passthrough": "desktop-vm-gpu",
		"":            "desktop-lxc", // умолчание: контейнер, хост не теряет консоль
	}
	for graphics, want := range cases {
		got, err := Resolve("desktop", graphics)
		if err != nil {
			t.Fatalf("graphics=%q: %v", graphics, err)
		}
		if got != want {
			t.Errorf("graphics=%q → %s, ожидалось %s", graphics, got, want)
		}
	}

	if _, err := Resolve("desktop", "непонятно"); err == nil {
		t.Error("неизвестное значение graphics принято")
	}
	// Обычный профиль ролью не подменяется.
	if got, _ := Resolve("haos", ""); got != "haos" {
		t.Errorf("обычный профиль подменён: %s", got)
	}
}

func TestLoadUnknownProfileListsKnown(t *testing.T) {
	_, err := Load("нет-такого")
	if err == nil {
		t.Fatal("неизвестный профиль принят")
	}
	if got := err.Error(); !contains(got, "haos") {
		t.Errorf("ошибка не перечисляет доступные профили: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestDefaultsMatchProxmoxExpectations(t *testing.T) {
	p, err := Load("haos")
	if err != nil {
		t.Fatal(err)
	}
	if p.VM.BIOSOr() != "ovmf" {
		t.Errorf("Home Assistant OS грузится через UEFI: bios = %q", p.VM.BIOSOr())
	}
	if !p.VM.EFIDisk {
		t.Error("для ovmf нужен EFI-диск")
	}
	if p.Image.Compressed != "xz" {
		t.Errorf("образ HAOS сжат xz: %q", p.Image.Compressed)
	}

	d, err := Load("desktop-lxc")
	if err != nil {
		t.Fatal(err)
	}
	if !d.LXC.UnprivilegedOr() {
		t.Error("рабочий стол должен быть непривилегированным контейнером")
	}
	if !d.LXC.DRI {
		t.Error("у рабочего стола в контейнере должен быть доступ к /dev/dri")
	}
}
