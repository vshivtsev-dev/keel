package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/plan"
)

func newRunner(t *testing.T) (*Runner, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	out := &bytes.Buffer{}
	return &Runner{
		Out:       out,
		Log:       &bytes.Buffer{},
		BackupDir: filepath.Join(root, "backups"),
		Stamp:     "2026-09-18_120301",
	}, out, root
}

func TestDryRunChangesNothing(t *testing.T) {
	r, out, root := newRunner(t)
	r.DryRun = true
	target := filepath.Join(root, "etc", "apt", "sources.list")

	step := plan.Step{ID: "x", Summary: "переписать репозитории", Action: plan.ActionWrite,
		Path: target, Content: []byte("deb http://example\n"), Diff: "+deb http://example"}

	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("в режиме показа файл всё же записан")
	}
	if !strings.Contains(out.String(), "переписать репозитории") {
		t.Errorf("шаг не показан: %q", out.String())
	}
}

func TestWriteBacksUpBeforeChanging(t *testing.T) {
	r, _, root := newRunner(t)
	target := filepath.Join(root, "etc", "pve", "storage.cfg")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("было\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	step := plan.Step{ID: "x", Summary: "поправить", Action: plan.ActionWrite,
		Path: target, Content: []byte("стало\n")}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(target)
	if err != nil || string(got) != "стало\n" {
		t.Fatalf("файл записан неверно: %q %v", got, err)
	}

	backup := filepath.Join(r.BackupDir, r.Stamp, target)
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("копия не снята: %v", err)
	}
	if string(saved) != "было\n" {
		t.Errorf("в копии не то, что было: %q", saved)
	}
}

// Конфиг без перевода строки в конце — источник тихих сюрпризов.
func TestWriteAddsTrailingNewline(t *testing.T) {
	r, _, root := newRunner(t)
	target := filepath.Join(root, "f.conf")
	step := plan.Step{ID: "x", Summary: "записать", Action: plan.ActionWrite,
		Path: target, Content: []byte("без перевода строки")}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if !strings.HasSuffix(string(got), "\n") {
		t.Errorf("перевод строки не дописан: %q", got)
	}
}

func TestConfirmSkipLeavesSystemAlone(t *testing.T) {
	r, _, root := newRunner(t)
	r.Confirm = func(plan.Step, string) Decision { return Skip }
	target := filepath.Join(root, "f.conf")

	step := plan.Step{ID: "x", Summary: "записать", Action: plan.ActionWrite,
		Path: target, Content: []byte("данные\n")}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatalf("пропуск не должен быть ошибкой: %v", err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("пропущенный шаг всё же выполнен")
	}
}

func TestConfirmAbortStopsEverything(t *testing.T) {
	r, _, root := newRunner(t)
	r.Confirm = func(plan.Step, string) Decision { return Abort }

	step := plan.Step{ID: "x", Summary: "записать", Action: plan.ActionWrite,
		Path: filepath.Join(root, "f.conf"), Content: []byte("данные\n")}
	err := r.Do(context.Background(), step)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("прерывание не распознано: %v", err)
	}
}

// Человек видит команду до того, как она выполнится, — и видит её без
// секрета, даже если секрет стоит прямо в аргументах.
func TestConfirmSeesMaskedBody(t *testing.T) {
	r, _, _ := newRunner(t)
	r.Mask = func(s string) string { return strings.ReplaceAll(s, "супертайна", "********") }

	var shown string
	r.Confirm = func(_ plan.Step, body string) Decision { shown = body; return Skip }

	step := plan.Step{ID: "x", Summary: "задать пароль", Action: plan.ActionExec,
		Cmd: []string{"pct", "exec", "102", "--", "chpasswd", "супертайна"}}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shown, "супертайна") {
		t.Fatalf("секрет показан человеку: %s", shown)
	}
	if !strings.Contains(shown, "pct exec 102") {
		t.Errorf("замаскировано лишнее: %s", shown)
	}
}

func TestExecRunsAndCapturesOutput(t *testing.T) {
	r, out, _ := newRunner(t)
	step := plan.Step{ID: "x", Summary: "поздороваться", Action: plan.ActionExec,
		Cmd: []string{"echo", "привет"}}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "привет") {
		t.Errorf("вывод команды не показан: %q", out.String())
	}
}

// Молчащий экран на упавшей команде — худшее, что может случиться:
// человек решит, что всё прошло, и пойдёт дальше.
func TestExecReportsFailureWithCommand(t *testing.T) {
	r, _, _ := newRunner(t)
	step := plan.Step{ID: "x", Summary: "сломаться", Action: plan.ActionExec,
		Cmd: []string{"false"}}
	err := r.Do(context.Background(), step)
	if err == nil {
		t.Fatal("упавшая команда не вызвала ошибки")
	}
	if !strings.Contains(err.Error(), "false") {
		t.Errorf("в ошибке не видно команды: %v", err)
	}
}

func TestMkdirCreatesPath(t *testing.T) {
	r, _, root := newRunner(t)
	target := filepath.Join(root, "mnt", "media")
	step := plan.Step{ID: "x", Summary: "создать каталог", Action: plan.ActionMkdir, Path: target}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		t.Fatalf("каталог не создан: %v", err)
	}
}

func TestEmptyCommandIsRefused(t *testing.T) {
	r, _, _ := newRunner(t)
	step := plan.Step{ID: "x", Summary: "ничего", Action: plan.ActionExec}
	if err := r.Do(context.Background(), step); err == nil {
		t.Fatal("пустая команда принята")
	}
}

// Ради KEEL_FS_ROOT всё и затевалось: провайдер называет настоящий путь,
// но при заданном отображении запись не должна дотянуться до живой машины.
// Раньше этого не было ни в Go, ни в bash: там прикрытием служила заглушка
// mkdir в тестах, то есть прикрытия не было вовсе.
func TestSystemPathsStayInsideSandbox(t *testing.T) {
	r, _, root := newRunner(t)
	r.Sys = func(p string) string { return filepath.Join(root, p) }

	steps := []plan.Step{
		{ID: "d", Summary: "создать каталог", Action: plan.ActionMkdir, Path: "/mnt/media"},
		{ID: "w", Summary: "записать конфиг", Action: plan.ActionWrite,
			Path: "/etc/apt/sources.list", Content: []byte("deb http://example\n")},
	}
	for _, s := range steps {
		if err := r.Do(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
	}

	for _, real := range []string{"/mnt/media", "/etc/apt/sources.list"} {
		if _, err := os.Stat(filepath.Join(root, real)); err != nil {
			t.Errorf("в песочнице нет %s: %v", real, err)
		}
	}
	// Сам факт, что тест идёт не под root, живую машину бы и так прикрыл —
	// поэтому проверяем не отсутствие файла снаружи, а присутствие внутри
	// и то, что путь в описании шага остался настоящим.
	if steps[1].Path != "/etc/apt/sources.list" {
		t.Errorf("путь в шаге подменён: %s", steps[1].Path)
	}
}

// Копия снимается с того же файла, который и правится, — иначе при
// KEEL_FS_ROOT keel читал бы живую машину.
func TestBackupFollowsSandbox(t *testing.T) {
	r, _, root := newRunner(t)
	r.Sys = func(p string) string { return filepath.Join(root, p) }

	inside := filepath.Join(root, "etc", "pve", "storage.cfg")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("было\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	step := plan.Step{ID: "w", Summary: "поправить", Action: plan.ActionWrite,
		Path: "/etc/pve/storage.cfg", Content: []byte("стало\n")}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}

	saved, err := os.ReadFile(filepath.Join(r.BackupDir, r.Stamp, "/etc/pve/storage.cfg"))
	if err != nil {
		t.Fatalf("копия не снята: %v", err)
	}
	if string(saved) != "было\n" {
		t.Errorf("в копии не то, что было: %q", saved)
	}
}

// KEEL_FS_ROOT должен быть настоящей песочницей, а не половинчатой.
// Уводить в сторону файлы и при этом по-настоящему звать pvesm — худшее
// из сочетаний: файлы лягут во временный каталог, а хост изменится
// взаправду. В bash от этого спасали заглушки в PATH, то есть только
// внутри тестов.
func TestSandboxDoesNotRunCommands(t *testing.T) {
	r, _, root := newRunner(t)
	r.Sandboxed = true
	r.Sys = func(p string) string { return filepath.Join(root, p) }

	marker := filepath.Join(root, "команда-выполнилась")
	step := plan.Step{ID: "x", Summary: "тронуть хост", Action: plan.ActionExec,
		Cmd: []string{"touch", marker}}

	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("в песочнице команда всё же выполнилась")
	}
	// Путь кириллический, поэтому Render его закавычит — так же, как это
	// делала bash-версия: команду должно быть можно скопировать в терминал.
	if got := r.Recorded(); len(got) != 1 || got[0] != Render(step.Cmd) {
		t.Errorf("команда не записана: %v", got)
	}
}

// А записи файлов в песочнице происходят по-настоящему — иначе проверять
// было бы нечего.
func TestSandboxStillWritesFiles(t *testing.T) {
	r, _, root := newRunner(t)
	r.Sandboxed = true
	r.Sys = func(p string) string { return filepath.Join(root, p) }

	step := plan.Step{ID: "w", Summary: "записать", Action: plan.ActionWrite,
		Path: "/etc/apt/sources.list.d/pve.sources", Content: []byte("Types: deb\n")}
	if err := r.Do(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/apt/sources.list.d/pve.sources")); err != nil {
		t.Errorf("файл в песочнице не записан: %v", err)
	}
}
