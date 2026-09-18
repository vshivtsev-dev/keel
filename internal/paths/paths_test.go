package paths

import (
	"path/filepath"
	"testing"
)

func TestSysWithoutRootReturnsPathAsIs(t *testing.T) {
	p := NewAt("/root/keel", "")
	if got := p.Sys("/etc/apt/sources.list"); got != "/etc/apt/sources.list" {
		t.Errorf("Sys = %q, ожидался исходный путь", got)
	}
	if p.Sandboxed() {
		t.Error("без KEEL_FS_ROOT песочницы быть не должно")
	}
}

// Ради этого fsroot() и существует: в тестах ни один системный путь не
// должен указывать на живую машину.
func TestSysWithRootStaysInside(t *testing.T) {
	root := t.TempDir()
	p := NewAt(filepath.Join(root, "home"), root)
	got := p.Sys("/etc/apt/sources.list")
	want := filepath.Join(root, "etc", "apt", "sources.list")
	if got != want {
		t.Errorf("Sys = %q, ожидалось %q", got, want)
	}
	if !p.Sandboxed() {
		t.Error("с KEEL_FS_ROOT должна быть песочница")
	}
}

func TestHomePathsSitTogether(t *testing.T) {
	t.Setenv("KEEL_MANIFEST", "")
	t.Setenv("KEEL_LOG_DIR", "")
	t.Setenv("KEEL_SECRETS_DIR", "")
	t.Setenv("KEEL_BACKUP_DIR", "")
	t.Setenv("KEEL_PLANS_DIR", "")
	p := NewAt("/root/keel", "")
	for _, got := range []string{p.Manifest(), p.Logs(), p.Secrets(), p.Backups(), p.Plans()} {
		if filepath.Dir(got) != "/root/keel" && got != "/root/keel/host.json" {
			t.Errorf("%q лежит вне /root/keel", got)
		}
	}
}
