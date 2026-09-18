// Package paths держит все пути keel в одном месте.
//
// Два разных корня, и путать их нельзя:
//
//	Home — каталог человека (/root/keel): манифест, секреты, копии, логи,
//	       планы. Обновление keel его не задевает.
//	Sys  — путь в системе (/etc/apt/..., /etc/pve/...). В обычной работе
//	       возвращается как есть, но если задан KEEL_FS_ROOT, всё уезжает
//	       во временный каталог: так тесты гоняются по-настоящему, но мимо
//	       живой системы. Прямой наследник fsroot() из lib/core.sh.
package paths

import (
	"os"
	"path/filepath"
)

type Paths struct {
	home   string
	fsRoot string
}

// New собирает пути из окружения. Каждый переопределяется отдельно —
// этим пользуются тесты.
func New() Paths {
	return Paths{
		home:   env("KEEL_HOME", "/root/keel"),
		fsRoot: os.Getenv("KEEL_FS_ROOT"),
	}
}

// NewAt собирает пути, целиком уведённые во временный каталог.
func NewAt(home, fsRoot string) Paths {
	return Paths{home: home, fsRoot: fsRoot}
}

func (p Paths) Home() string     { return p.home }
func (p Paths) FSRoot() string   { return p.fsRoot }
func (p Paths) Manifest() string { return env("KEEL_MANIFEST", filepath.Join(p.home, "host.json")) }
func (p Paths) Logs() string     { return env("KEEL_LOG_DIR", filepath.Join(p.home, "logs")) }
func (p Paths) Secrets() string  { return env("KEEL_SECRETS_DIR", filepath.Join(p.home, "secrets")) }
func (p Paths) Backups() string  { return env("KEEL_BACKUP_DIR", filepath.Join(p.home, "backups")) }
func (p Paths) Plans() string    { return env("KEEL_PLANS_DIR", filepath.Join(p.home, "plans")) }

// Sys возвращает системный путь с учётом KEEL_FS_ROOT.
func (p Paths) Sys(path string) string {
	if p.fsRoot == "" {
		return path
	}
	return filepath.Join(p.fsRoot, path)
}

// Sandboxed сообщает, уведены ли системные пути в сторону от живой машины.
func (p Paths) Sandboxed() bool { return p.fsRoot != "" }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
