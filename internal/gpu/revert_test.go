package gpu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vshivtsev-dev/keel/internal/plan"
)

func sample(stamp string) *State {
	return &State{
		Created: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Device:  "0000:64:00.0", IDs: "1002:15bf", VM: "201",
		Stamp: stamp, Bootloader: "systemd-boot (через proxmox-boot-tool)",
		Changed:      []string{"/etc/kernel/cmdline", "/etc/modules"},
		CreatedFiles: []string{"/etc/modprobe.d/keel-vfio.conf"},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	want := sample("2026-09-18_120000")
	if err := Save(home, want); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("права на запись о пробросе %v, ожидалось 0600", st.Mode().Perm())
	}

	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != want.Device || got.Stamp != want.Stamp || len(got.Changed) != 2 {
		t.Errorf("запись прочитана неверно: %+v", got)
	}
}

func TestLoadSaysWhereToLookWhenMissing(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "backups") {
		t.Errorf("ошибка не подсказывает, где искать: %v", err)
	}
}

// Файлы, существовавшие до нас, возвращаются из копий; те, что создал
// keel, удаляются. Перепутать эти две группы — значит оставить хост с
// файлом vfio и без картинки.
func TestRevertRestoresAndRemoves(t *testing.T) {
	root := t.TempDir()
	backups := filepath.Join(root, "backups")
	stamp := "2026-09-18_120000"

	for path, body := range map[string]string{
		"/etc/kernel/cmdline": "root=ZFS=rpool/ROOT/pve-1 boot=zfs\n",
		"/etc/modules":        "loop\n",
	} {
		full := filepath.Join(backups, stamp, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Файл, который keel создал сам.
	created := filepath.Join(root, "sys/etc/modprobe.d/keel-vfio.conf")
	if err := os.MkdirAll(filepath.Dir(created), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("options vfio-pci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sys := func(p string) string { return filepath.Join(root, "sys", p) }

	p, err := RevertPlan(sample(stamp), backups, sys, true)
	if err != nil {
		t.Fatal(err)
	}

	var restored, removed []string
	for _, s := range p.Steps {
		switch s.Action {
		case plan.ActionWrite:
			restored = append(restored, s.Path)
			if len(s.Content) == 0 {
				t.Errorf("%s возвращается пустым — содержимое копии потеряно", s.Path)
			}
		case plan.ActionExec:
			if s.Cmd[0] == "rm" {
				removed = append(removed, s.Cmd[len(s.Cmd)-1])
			}
		}
	}
	if len(restored) != 2 {
		t.Errorf("возвращается %d файлов, ожидалось 2: %v", len(restored), restored)
	}
	if len(removed) != 1 || removed[0] != "/etc/modprobe.d/keel-vfio.conf" {
		t.Errorf("удаляется не то: %v", removed)
	}

	// Загрузчик и initramfs надо пересобрать, иначе правки не вступят в силу.
	joined := ""
	for _, s := range p.Steps {
		if s.Action == plan.ActionExec {
			joined += strings.Join(s.Cmd, " ") + "\n"
		}
	}
	for _, want := range []string{"proxmox-boot-tool refresh", "update-initramfs -u -k all",
		"qm set 201 --delete hostpci0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("в откате нет %q:\n%s", want, joined)
		}
	}
}

// Содержимое файла до правки keel не выдумает, и делать вид, что откат
// удался, нельзя.
func TestRevertRefusesWithoutBackup(t *testing.T) {
	_, err := RevertPlan(sample("нет-такой-метки"), t.TempDir(), nil, false)
	if err == nil {
		t.Fatal("откат собрался без резервных копий")
	}
	if !strings.Contains(err.Error(), "правь руками") {
		t.Errorf("ошибка не говорит, что делать: %v", err)
	}
}

// Забирать hostpci0 у несуществующей ВМ незачем.
func TestRevertSkipsMissingVM(t *testing.T) {
	root := t.TempDir()
	backups := filepath.Join(root, "backups")
	stamp := "s"
	for _, path := range []string{"/etc/kernel/cmdline", "/etc/modules"} {
		full := filepath.Join(backups, stamp, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x\n"), 0o600)
	}
	st := sample(stamp)

	p, err := RevertPlan(st, backups, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Steps {
		if strings.Contains(strings.Join(s.Cmd, " "), "hostpci0") {
			t.Errorf("keel забирает карту у несуществующей ВМ: %v", s.Cmd)
		}
	}
}

// Инструкцию читают с погасшего монитора, то есть не читают вовсе —
// значит в ней должны быть ровно те команды, которые спасают.
func TestRevertInstructionsAreActionable(t *testing.T) {
	got := RevertInstructions("/root/keel", "/root/keel/backups", "/etc/modprobe.d/keel-vfio.conf")
	for _, want := range []string{"keel gpu revert", "live-USB", "rm /mnt/etc/modprobe.d/keel-vfio.conf",
		"/root/keel/gpu-passthrough.state"} {
		if !strings.Contains(got, want) {
			t.Errorf("в инструкции нет %q:\n%s", want, got)
		}
	}
}
