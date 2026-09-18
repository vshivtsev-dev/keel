package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

const cbManifest = `{"host":{"config_backup":{"path":"/var/lib/vz/dump/keel-config","keep":3,"max_age_hours":24}}}`

// hostWithConfig собирает подставной хост с настоящими файлами: провайдер
// спрашивает у фактов, что из списка на нём есть, а факты читают диск.
func hostWithConfig(t *testing.T, archives ...facts.Archive) *facts.Facts {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"etc/pve/storage.cfg", "etc/network/interfaces", "etc/hostname"} {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("данные\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &facts.Facts{
		Report:         "# Снимок хоста\n",
		ConfigArchives: archives,
		SysForPaths:    func(p string) string { return filepath.Join(root, p) },
	}
}

func cbProvider() ConfigBackup {
	// Время замораживаем: имя архива входит в команду, и без этого
	// сверять её было бы нечем.
	return ConfigBackup{Now: func() time.Time {
		return time.Date(2026, 9, 18, 12, 3, 1, 0, time.UTC)
	}}
}

func cbPlan(t *testing.T, f *facts.Facts) plan.Changes {
	t.Helper()
	c, err := cbProvider().Plan(context.Background(), parse(t, cbManifest), f)
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func TestConfigBackupMakesFirstArchive(t *testing.T) {
	c := cbPlan(t, hostWithConfig(t))

	var tarCmd string
	for _, cmd := range commands(c.Steps) {
		if strings.HasPrefix(cmd, "tar ") {
			tarCmd = cmd
		}
	}
	if tarCmd == "" {
		t.Fatalf("архив не собирается: %v", commands(c.Steps))
	}
	for _, want := range []string{
		"keel-host-2026-09-18_120301.tar.gz",
		"--ignore-failed-read",
		"etc/pve",
		"etc/network/interfaces",
		"etc/hostname",
	} {
		if !strings.Contains(tarCmd, want) {
			t.Errorf("в команде сборки нет %q:\n%s", want, tarCmd)
		}
	}
	// Того, чего на хосте нет, в архиве быть не должно: tar иначе ругается
	// на каждый отсутствующий путь.
	if strings.Contains(tarCmd, "etc/vzdump.conf") {
		t.Errorf("в архив попал несуществующий путь:\n%s", tarCmd)
	}
}

// Без снимка состояния архив — просто набор конфигов без объяснения, от
// какой он машины.
func TestConfigBackupIncludesHostSnapshot(t *testing.T) {
	c := cbPlan(t, hostWithConfig(t))

	var report plan.Step
	for _, s := range c.Steps {
		if s.Action == plan.ActionWrite && strings.HasSuffix(s.Path, "host-report.txt") {
			report = s
		}
	}
	if report.ID == "" {
		t.Fatalf("снимок состояния не кладётся в архив: %+v", c.Steps)
	}
	if !strings.Contains(string(report.Content), "Снимок хоста") {
		t.Errorf("снимок пуст: %q", report.Content)
	}
	// Собирать архив до того, как снимок записан, бессмысленно.
	for _, s := range c.Steps {
		if strings.HasSuffix(s.ID, ":tar") {
			if !contains(s.Needs, report.ID) {
				t.Errorf("сборка архива не ждёт снимка: %+v", s.Needs)
			}
		}
	}
}

// Свежая копия есть — делать нечего. Иначе keel собирал бы архив при
// каждом запуске.
func TestConfigBackupSkipsWhenFresh(t *testing.T) {
	f := hostWithConfig(t, facts.Archive{
		Path: "/var/lib/vz/dump/keel-config/keel-host-2026-09-18_100000.tar.gz",
		Age:  2 * time.Hour,
	})
	if c := cbPlan(t, f); len(c.Steps) != 0 {
		t.Errorf("при свежей копии появились шаги: %v", commands(c.Steps))
	}
}

func TestConfigBackupRemakesWhenStale(t *testing.T) {
	f := hostWithConfig(t, facts.Archive{
		Path: "/var/lib/vz/dump/keel-config/keel-host-2026-09-15_100000.tar.gz",
		Age:  70 * time.Hour,
	})
	if c := cbPlan(t, f); len(c.Steps) == 0 {
		t.Error("устаревшая копия не вызвала пересборки")
	}
}

// Прореживание: keep=3 значит три копии всего, считая ту, что соберётся
// сейчас, — поэтому лишними становятся начиная с третьей старой.
func TestConfigBackupPrunesOldArchives(t *testing.T) {
	var archives []facts.Archive
	for i := 0; i < 5; i++ {
		archives = append(archives, facts.Archive{
			Path: filepath.Join("/var/lib/vz/dump/keel-config",
				"keel-host-старый-"+string(rune('a'+i))+".tar.gz"),
			Age: time.Duration(48+i*24) * time.Hour,
		})
	}
	c := cbPlan(t, hostWithConfig(t, archives...))

	var removed []string
	for _, cmd := range commands(c.Steps) {
		if strings.HasPrefix(cmd, "rm -f ") {
			removed = append(removed, cmd)
		}
	}
	if len(removed) != 3 {
		t.Fatalf("удаляется %d копий, ожидалось 3: %v", len(removed), removed)
	}
	// Самые свежие две остаются.
	for _, cmd := range removed {
		if strings.Contains(cmd, "старый-a") || strings.Contains(cmd, "старый-b") {
			t.Errorf("удаляется свежая копия: %s", cmd)
		}
	}
	// Удалять старое до того, как собран новый архив, нельзя.
	for _, s := range c.Steps {
		if strings.Contains(s.ID, ":prune:") && !contains(s.Needs, "host/config-backup:tar") {
			t.Errorf("прореживание не ждёт сборки архива: %+v", s.Needs)
		}
	}
}

// Копия рядом с оригиналом спасает только от опечаток.
func TestConfigBackupWarnsAboutKeepingCopyElsewhere(t *testing.T) {
	c := cbPlan(t, hostWithConfig(t))
	if len(c.Notes) != 1 || !strings.Contains(c.Notes[0].Message, "не только на этом хосте") {
		t.Errorf("нет напоминания хранить копию в стороне: %+v", c.Notes)
	}
}

func TestConfigBackupRefusesOnNonProxmoxHost(t *testing.T) {
	f := &facts.Facts{SysForPaths: func(string) string { return filepath.Join(t.TempDir(), "пусто") }}
	_, err := cbProvider().Plan(context.Background(), parse(t, cbManifest), f)
	if err == nil {
		t.Fatal("на хосте без конфигов keel собрался делать архив")
	}
}

func TestConfigBackupRuleOfZero(t *testing.T) {
	if (ConfigBackup{}).Configured(parse(t, `{"host":{}}`)) {
		t.Error("без ключа config_backup провайдер объявил себя настроенным")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
