package host

import (
	"context"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Хост, каким его видели тесты bash-версии: local типа dir и local-lvm,
// которого в манифесте нет.
func hostWithStorages() *facts.Facts {
	return &facts.Facts{
		Hostname: "pve-01",
		Storages: []facts.Storage{
			{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: []string{"backup", "iso", "vztmpl"}},
			{Name: "local-lvm", Type: "lvmthin", Content: []string{"images", "rootdir"}},
		},
	}
}

func parse(t *testing.T, body string) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(body))
	if err != nil {
		t.Fatalf("манифест не разобрался: %v", err)
	}
	return m
}

// commands достаёт из плана команды в том виде, в каком они уйдут хосту.
// Это и есть сверка, ради которой в bash-версии стояли заглушки
// qm/pct/pvesm: место, где живёт большинство ошибок, — сборка команды.
func commands(steps []plan.Step) []string {
	var out []string
	for _, s := range steps {
		if s.Action == plan.ActionExec {
			out = append(out, exec.Render(s.Cmd))
		}
	}
	return out
}

func planOf(t *testing.T, m *manifest.Manifest, f *facts.Facts) plan.Changes {
	t.Helper()
	c, err := (Storage{}).Plan(context.Background(), m, f)
	if err != nil {
		t.Fatalf("сборка плана не удалась: %v", err)
	}
	return c
}

func assertCommands(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("команд %d, ожидалось %d:\n  получено: %v\n  ожидалось: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("команда %d:\n  получено:  %s\n  ожидалось: %s", i, got[i], want[i])
		}
	}
}

func TestStorageAddsMissingContent(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"local","content":["iso","vztmpl","backup","snippets"]}]}`)
	c := planOf(t, m, hostWithStorages())

	// Передаётся объединение, а не одно недостающее: pvesm set заменяет
	// набор целиком, и «--content snippets» стёрло бы всё остальное.
	assertCommands(t, commands(c.Steps), []string{
		"pvesm set local --content backup,iso,snippets,vztmpl",
	})
	if len(c.Notes) != 0 {
		t.Errorf("появились лишние заметки: %+v", c.Notes)
	}
	if !strings.Contains(c.Steps[0].Summary, "snippets") {
		t.Errorf("в описании шага не видно, что именно добавляется: %q", c.Steps[0].Summary)
	}
}

// Порядок в списке — не расхождение. Иначе keel гонял бы pvesm вхолостую
// при каждом запуске.
func TestStorageIgnoresContentOrder(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"local","content":["vztmpl","backup","iso"]}]}`)
	c := planOf(t, m, hostWithStorages())
	if !c.Empty() {
		t.Errorf("порядок в списке принят за расхождение: %+v", c)
	}
}

func TestStorageCreatesDirStorage(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"media","type":"dir","path":"/mnt/media","content":["iso"]}]}`)
	c := planOf(t, m, hostWithStorages())

	assertCommands(t, commands(c.Steps), []string{
		"pvesm add dir media --path /mnt/media --content iso",
	})

	// Каталог создаётся отдельным шагом, и хранилище ждёт его: pvesm примет
	// хранилище с несуществующим каталогом, а работать оно не будет.
	if len(c.Steps) != 2 || c.Steps[0].Action != plan.ActionMkdir {
		t.Fatalf("каталог не создаётся отдельным шагом: %+v", c.Steps)
	}
	if c.Steps[0].Path != "/mnt/media" {
		t.Errorf("каталог создаётся не там: %q", c.Steps[0].Path)
	}
	if len(c.Steps[1].Needs) != 1 || c.Steps[1].Needs[0] != c.Steps[0].ID {
		t.Errorf("хранилище не ждёт каталог: %+v", c.Steps[1].Needs)
	}
}

// LVM и ZFS keel не заводит: слишком опасно решать это за человека.
// Раньше об этом сообщала строка, которую план выдавал за изменение.
// Теперь это заметка — видно, что keel знает о расхождении и не тронет его.
func TestStorageRefusesToInventLVM(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"tank","content":["images"]}]}`)
	c := planOf(t, m, hostWithStorages())

	if len(c.Steps) != 0 {
		t.Fatalf("keel собрался создавать не-dir хранилище: %v", commands(c.Steps))
	}
	if len(c.Notes) != 1 {
		t.Fatalf("о невозможности создать хранилище не сказано: %+v", c.Notes)
	}
	if !strings.Contains(c.Notes[0].Message, "type=dir") {
		t.Errorf("заметка не объясняет, что делать: %q", c.Notes[0].Message)
	}
}

// Правило «ничего вне манифеста не трогается» — в плане не должно быть
// ни одного упоминания local-lvm.
func TestStorageNeverTouchesUnlisted(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"local","content":["iso","snippets"]}]}`)
	c := planOf(t, m, hostWithStorages())

	for _, cmd := range commands(c.Steps) {
		if strings.Contains(cmd, "local-lvm") {
			t.Errorf("хранилище вне манифеста попало в команду: %s", cmd)
		}
	}
}

// Ничего не удаляется: типы, которых нет в манифесте, но есть на хосте,
// должны остаться на месте.
func TestStorageNeverRemovesContent(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"local","content":["snippets"]}]}`)
	c := planOf(t, m, hostWithStorages())

	assertCommands(t, commands(c.Steps), []string{
		"pvesm set local --content backup,iso,snippets,vztmpl",
	})
}

func TestStorageRuleOfZero(t *testing.T) {
	m := parse(t, `{}`)
	if (Storage{}).Configured(m) {
		t.Error("без ключа storages провайдер объявил себя настроенным")
	}
}

func TestStorageRejectsNamelessEntry(t *testing.T) {
	m := parse(t, `{"storages":[{"content":["iso"]}]}`)
	_, err := (Storage{}).Plan(context.Background(), m, hostWithStorages())
	if err == nil {
		t.Fatal("хранилище без имени принято молча")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("ошибка не называет пропущенный ключ: %v", err)
	}
}

func TestStorageVerify(t *testing.T) {
	m := parse(t, `{"storages":[{"name":"local","content":["iso","snippets"]},{"name":"нет-такого","content":["iso"]}]}`)
	findings, err := (Storage{}).Verify(context.Background(), m, hostWithStorages())
	if err != nil {
		t.Fatal(err)
	}

	by := map[string]plan.Finding{}
	for _, f := range findings {
		by[f.Resource] = f
	}

	if f := by["хранилище local"]; f.OK {
		t.Errorf("нехватка snippets не замечена: %+v", f)
	} else if !strings.Contains(f.Message, "snippets") {
		t.Errorf("не сказано, чего не хватает: %q", f.Message)
	}

	if f := by["хранилище нет-такого"]; f.OK {
		t.Errorf("отсутствующее хранилище объявлено исправным: %+v", f)
	}

	// О хранилище вне манифеста keel рассказывает, но расхождением его
	// не считает — трогать его он всё равно не станет.
	f := by["хранилище local-lvm"]
	if !f.Unmanaged || !f.OK {
		t.Errorf("хранилище вне манифеста помечено неверно: %+v", f)
	}
}
