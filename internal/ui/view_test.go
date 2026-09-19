package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/cli"
	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Снимки сравниваются без цвета: иначе тест проверял бы возможности
// терминала, на котором его запустили, а не то, что нарисовал keel.
func init() { os.Setenv("NO_COLOR", "1") }

// testModel собирает экран на готовых данных, не трогая ни сеть, ни хост.
func testModel(t *testing.T, width, height int) *Model {
	return newTestModel(t, width, height, true)
}

// newTestModel: sandboxed=false нужен там, где команды должны и правда
// выполняться — иначе песочница их только записывает, и упавший шаг
// выглядит успешным.
func newTestModel(t *testing.T, width, height int, sandboxed bool) *Model {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	mpath := filepath.Join(home, "host.json")
	if err := os.WriteFile(mpath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sys := ""
	if sandboxed {
		sys = filepath.Join(root, "sys")
	}
	p := paths.NewAt(home, sys)
	app := cli.NewApp(os.Stdout, p, cli.Options{Mode: cli.ModeStep}, "0.3.0")
	app.Capturer = exec.NewFake()
	t.Cleanup(func() { _ = app.Close() })

	m := New(context.Background(), app)
	m.width, m.height = width, height
	m.manifest = &manifest.Manifest{Path: mpath}
	m.facts = &facts.Facts{
		Hostname:   "pve-01",
		PVEVersion: "pve-manager/9.0.3/abcdef (running kernel: 6.14.0-2-pve)",
		CPUModel:   "AMD Ryzen 7 8745HS",
		IOMMU:      true,
		Upgradable: 12,
		GPUs:       []facts.GPU{{Address: "0000:64:00.0", Desc: "AMD Radeon 780M", Driver: "amdgpu"}},
		Storages: []facts.Storage{
			{Name: "local", Type: "dir", Content: []string{"backup", "iso"}},
			{Name: "local-lvm", Type: "lvmthin"},
		},
	}
	m.results = []engine.Result{
		{Provider: "host/repos", Title: "Репозитории Proxmox", Status: engine.StatusOK},
		{Provider: "host/updates", Title: "Обновление пакетов", Status: engine.StatusChanges,
			Steps: []plan.Step{{ID: "u1", Provider: "host/updates",
				Summary: "установить обновления: 12, 340 МБ", Action: plan.ActionExec,
				Cmd: []string{"apt-get", "-y", "dist-upgrade"}}}},
		{Provider: "host/storage", Title: "Хранилища", Status: engine.StatusChanges,
			Steps: []plan.Step{{ID: "s1", Provider: "host/storage",
				Summary: "добавить content: snippets", Action: plan.ActionExec,
				Cmd: []string{"pvesm", "set", "local", "--content", "backup,iso,snippets"}}}},
		{Provider: "host/backup", Title: "Резервное копирование гостей",
			Status: engine.StatusUnconfigured},
		{Provider: "guests", Title: "Гости: виртуальные машины и контейнеры",
			Status: engine.StatusOK,
			Notes: []plan.Note{{Resource: "100 «haos»",
				Message: "уже есть на хосте — keel его не трогает"}}},
	}
	m.plan = &plan.Plan{Version: plan.FormatVersion}
	for _, r := range m.results {
		m.plan.Steps = append(m.plan.Steps, r.Steps...)
		if r.Status == engine.StatusChanges {
			m.chosen[r.Provider] = true
		}
	}
	m.rows = buildRows(m.results, m.expanded)
	m.stage = stageChoosing
	// То же, что делает настоящая сборка фактов: строка состояния
	// уступает место подсказкам.
	m.status = ""
	m.cursorToFirstChange()
	return m
}

func TestWideLayoutShowsAllZones(t *testing.T) {
	m := testModel(t, 120, 30)
	view := m.View()

	for _, want := range []string{
		"keel 0.3.0", "pve-01", // шапка
		"Что делаем", // рабочая зона
		"Хост",       // факты
		"Выбрано",    // сводка
		"Хранилища",  // строка дерева
		"AMD Radeon", // факты о видеокарте
		"[x]",        // отметка выбора
		"[a] применить",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("в широкой раскладке нет %q:\n%s", want, view)
		}
	}
	if lines := strings.Count(view, "\n") + 1; lines > 30 {
		t.Errorf("экран не влез в 30 строк: %d", lines)
	}
}

// Три состояния должны различаться на экране: их путаница скрывает от
// человека, что он забыл ключ в манифесте.
func TestStatusMarksAreDistinct(t *testing.T) {
	m := testModel(t, 120, 30)
	view := m.View()
	for _, want := range []string{"→", "✓", "·"} {
		if !strings.Contains(view, want) {
			t.Errorf("на экране нет значка %q:\n%s", want, view)
		}
	}
}

// Консоль Proxmox по IPMI и терминал на телефоне — ровно те места, где
// хост и чинят, когда всё сломалось.
func TestCompactLayoutFitsPhone(t *testing.T) {
	m := testModel(t, 45, 22)
	view := m.View()

	for _, line := range strings.Split(view, "\n") {
		if w := visibleWidth(line); w > 45 {
			t.Errorf("строка шире экрана (%d > 45):\n%q", w, line)
		}
	}
	if !strings.Contains(view, "pve-01") {
		t.Errorf("в аварийной раскладке нет имени хоста:\n%s", view)
	}
	if !strings.Contains(view, "Хранилища") {
		t.Errorf("в аварийной раскладке нет дерева:\n%s", view)
	}
	// Подсказки должны стать короче, а не обрезаться на полуслове.
	if strings.Contains(view, "пересчитать") {
		t.Errorf("в узкой раскладке остались длинные подсказки:\n%s", view)
	}
}

func TestVeryNarrowWindowSaysWhatToDo(t *testing.T) {
	m := testModel(t, 30, 8)
	if !strings.Contains(m.View(), "растяни") && !strings.Contains(m.View(), "Растяни") {
		t.Errorf("на крошечном окне нет подсказки:\n%s", m.View())
	}
}

// Ни одна строка не должна вылезать за край: разъехавшаяся рамка читается
// как сломанная программа.
func TestNoLineExceedsWidth(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {100, 24}, {80, 24}, {60, 20}, {45, 22}} {
		m := testModel(t, size[0], size[1])
		for _, line := range strings.Split(m.View(), "\n") {
			if w := visibleWidth(line); w > size[0] {
				t.Errorf("при ширине %d строка шириной %d:\n%q", size[0], w, line)
			}
		}
	}
}

func visibleWidth(s string) int {
	n, inEscape := 0, false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		n++
	}
	return n
}
