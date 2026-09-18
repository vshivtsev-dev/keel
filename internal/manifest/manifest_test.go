package manifest

import (
	"path/filepath"
	"runtime"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("не удалось определить путь к тесту")
	}
	return filepath.Join(filepath.Dir(self), "..", "..")
}

// Главная проверка порта: настоящий пример манифеста, который читал bash,
// должен читаться новым разбором без потерь. Если этот тест падает —
// существующие установки сломаются при обновлении.
func TestLoadExampleManifest(t *testing.T) {
	m, err := Load(filepath.Join(repoRoot(t), "manifest", "host.example.json"))
	if err != nil {
		t.Fatalf("пример манифеста не читается: %v", err)
	}

	if m.Host.Repos != "no-subscription" {
		t.Errorf("host.repos = %q, ожидалось no-subscription", m.Host.Repos)
	}
	if !m.Host.UpdatesEnabled() {
		t.Error("host.updates должно быть включено")
	}
	if got := m.Host.UpdatesMinSpeedOr(); got != "30K" {
		t.Errorf("host.updates_min_speed = %q, ожидалось 30K", got)
	}

	if m.Host.ConfigBackup == nil {
		t.Fatal("host.config_backup потерян")
	}
	if got := m.Host.ConfigBackup.KeepOr(); got != 7 {
		t.Errorf("config_backup.keep = %d, ожидалось 7", got)
	}

	if len(m.Storages) != 1 || m.Storages[0].Name != "local" {
		t.Fatalf("storages разобраны неверно: %+v", m.Storages)
	}
	if len(m.Storages[0].Content) != 4 {
		t.Errorf("storages[0].content = %v, ожидалось 4 типа", m.Storages[0].Content)
	}

	if len(m.Guests) != 3 {
		t.Fatalf("гостей %d, ожидалось 3", len(m.Guests))
	}
	if m.Guests[0].ID != 100 || m.Guests[0].Profile != "haos" {
		t.Errorf("guests[0] разобран неверно: %+v", m.Guests[0])
	}
	if m.Guests[1].Graphics != "dri" {
		t.Errorf("guests[1].graphics = %q, ожидалось dri", m.Guests[1].Graphics)
	}
	if m.Guests[1].CloudInit == nil || m.Guests[1].CloudInit.User != "av" {
		t.Errorf("guests[1].cloudinit разобран неверно: %+v", m.Guests[1].CloudInit)
	}
	if len(m.Guests[1].Packages) != 2 {
		t.Errorf("guests[1].packages = %v", m.Guests[1].Packages)
	}
	// image_version в примере закомментирован — значит его быть не должно.
	if m.Guests[0].ImageVersion != "" {
		t.Errorf("image_version взят из комментария: %q", m.Guests[0].ImageVersion)
	}

	if m.Backup == nil {
		t.Fatal("backup потерян")
	}
	if m.Backup.Schedule != "02:00" || m.Backup.KeepLastOr() != 3 {
		t.Errorf("backup разобран неверно: %+v", m.Backup)
	}
	if len(m.Backup.Guests) != 3 {
		t.Errorf("backup.guests = %v", m.Backup.Guests)
	}
}

// Правило нуля: пустой манифест корректен и означает «не трогай ничего».
func TestEmptyManifestIsValid(t *testing.T) {
	m, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("пустой манифест отвергнут: %v", err)
	}
	if m.Host.Repos != "" || m.Host.Updates != nil || m.Backup != nil {
		t.Errorf("в пустом манифесте появились значения: %+v", m)
	}
	if len(m.Storages) != 0 || len(m.Guests) != 0 {
		t.Errorf("в пустом манифесте появились списки: %+v", m)
	}
}

// Отличить «ключа нет» от «указан false» обязательно: на этом держится
// правило нуля. false значит «решено не обновлять», nil — «не спрашивали».
func TestAbsentDiffersFromFalse(t *testing.T) {
	absent, err := Parse([]byte(`{"host":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Parse([]byte(`{"host":{"updates":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Host.Updates != nil {
		t.Error("отсутствующий updates прочитан как значение")
	}
	if explicit.Host.Updates == nil || *explicit.Host.Updates {
		t.Error("явный updates:false прочитан неверно")
	}
}

// Опечатка в имени ключа не должна выглядеть как «делать нечего».
func TestUnknownKeyRejected(t *testing.T) {
	_, err := Parse([]byte(`{"host":{"repost":"no-subscription"}}`))
	if err == nil {
		t.Fatal("опечатка в ключе принята молча")
	}
}
