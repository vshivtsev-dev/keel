package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T) *Plan {
	t.Helper()
	return &Plan{
		Version:     FormatVersion,
		Created:     time.Date(2026, 9, 18, 12, 3, 1, 0, time.UTC),
		Host:        "pve-01",
		KeelVer:     "0.3.0",
		Manifest:    "/root/keel/host.json",
		ManifestS:   "манифест-отпечаток",
		FactsDigest: "хост-отпечаток",
		Steps: []Step{
			{ID: "host/storage:content:local", Provider: "host/storage",
				Resource: "хранилище local", Summary: "добавить content: snippets",
				Action: ActionExec, Cmd: []string{"pvesm", "set", "local", "--content", "backup,iso,snippets"}},
			{ID: "host/repos:write", Provider: "host/repos",
				Resource: "репозитории", Summary: "переключить на no-subscription",
				Action: ActionWrite, Path: "/etc/apt/sources.list.d/pve.sources"},
		},
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := sample(t)

	path, err := p.Save(dir)
	if err != nil {
		t.Fatalf("сохранение не удалось: %v", err)
	}
	if filepath.Base(path) != "2026-09-18_120301.json" {
		t.Errorf("имя файла плана: %s", filepath.Base(path))
	}

	// План описывает хост целиком — читать его посторонним незачем.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("права на файл плана %v, ожидалось 0600", st.Mode().Perm())
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("чтение не удалось: %v", err)
	}
	if len(got.Steps) != 2 || got.Steps[0].ID != p.Steps[0].ID {
		t.Fatalf("шаги не совпали: %+v", got.Steps)
	}
	if strings.Join(got.Steps[0].Cmd, " ") != "pvesm set local --content backup,iso,snippets" {
		t.Errorf("команда испорчена при сохранении: %v", got.Steps[0].Cmd)
	}
	if got.FactsDigest != p.FactsDigest {
		t.Errorf("отпечаток фактов потерян")
	}
}

func TestLoadRejectsForeignFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("план чужой версии принят")
	}
}

func TestLatestPicksNewest(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"2026-09-17_100000.json", "2026-09-18_120301.json", "2026-09-18_090000.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "2026-09-18_120301.json" {
		t.Errorf("выбран %s, ожидался самый свежий", filepath.Base(got))
	}
}

func TestLatestSaysWhatToDoWhenEmpty(t *testing.T) {
	_, err := Latest(t.TempDir())
	if err == nil {
		t.Fatal("пустой каталог планов не вызвал ошибки")
	}
	if !strings.Contains(err.Error(), "keel plan") {
		t.Errorf("ошибка не подсказывает, что делать: %v", err)
	}
}

// Ради этого план и стал артефактом: применяется ровно то, что посмотрели.
func TestStaleDetectsBothKindsOfDrift(t *testing.T) {
	p := sample(t)

	if stale, why := p.Stale("манифест-отпечаток", "хост-отпечаток"); stale {
		t.Errorf("свежий план объявлен устаревшим: %s", why)
	}
	if stale, why := p.Stale("другой-манифест", "хост-отпечаток"); !stale {
		t.Error("правка манифеста не замечена")
	} else if !strings.Contains(why, "манифест") {
		t.Errorf("непонятная причина: %s", why)
	}
	if stale, why := p.Stale("манифест-отпечаток", "другой-хост"); !stale {
		t.Error("изменение хоста не замечено")
	} else if !strings.Contains(why, "хост") {
		t.Errorf("непонятная причина: %s", why)
	}
}

func TestSelectNarrowsToChosenSteps(t *testing.T) {
	p := sample(t)

	if all := p.Select(nil); len(all.Steps) != 2 {
		t.Error("пустой выбор должен означать «всё»")
	}
	byStep := p.Select(map[string]bool{"host/repos:write": true})
	if len(byStep.Steps) != 1 || byStep.Steps[0].Provider != "host/repos" {
		t.Errorf("выбор по шагу: %+v", byStep.Steps)
	}
	byProvider := p.Select(map[string]bool{"host/storage": true})
	if len(byProvider.Steps) != 1 || byProvider.Steps[0].Provider != "host/storage" {
		t.Errorf("выбор по провайдеру: %+v", byProvider.Steps)
	}
	// Исходный план не должен пострадать: его ещё показывать на экране.
	if len(p.Steps) != 2 {
		t.Error("Select испортил исходный план")
	}
}

func TestProvidersKeepsOrder(t *testing.T) {
	p := sample(t)
	got := p.Providers()
	if len(got) != 2 || got[0] != "host/storage" || got[1] != "host/repos" {
		t.Errorf("порядок провайдеров: %v", got)
	}
}
