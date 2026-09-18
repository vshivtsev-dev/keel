// Package manifest читает host.json — описание того, каким должен быть хост.
//
// Главное правило проекта живёт здесь, в типах: нет ключа — нет действия.
// Поэтому необязательные скаляры описаны указателями: так «ключа нет»
// отличимо от «указан нуль». Пустая структура Manifest означает «не трогай
// ничего», и это корректный манифест, а не ошибка.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

type Manifest struct {
	Host     Host      `json:"host"`
	Storages []Storage `json:"storages"`
	Guests   []Guest   `json:"guests"`
	Backup   *Backup   `json:"backup"`

	// Path — откуда манифест прочитан. В JSON не пишется.
	Path string `json:"-"`
}

type Host struct {
	// Repos: no-subscription | enterprise | test. Пусто — не трогать.
	Repos string `json:"repos"`
	// Updates: nil — не трогать, false — явно не обновлять.
	Updates *bool `json:"updates"`
	// UpdatesMinSpeed: порог скорости до репозитория, например "30K".
	// Пусто — берётся значение по умолчанию, "0" — не проверять.
	UpdatesMinSpeed string          `json:"updates_min_speed"`
	ConfigBackup    *ConfigBackup   `json:"config_backup"`
	GPUPassthrough  *GPUPassthrough `json:"gpu_passthrough"`
}

type ConfigBackup struct {
	Path        string `json:"path"`
	Keep        *int   `json:"keep"`
	MaxAgeHours *int   `json:"max_age_hours"`
}

type GPUPassthrough struct {
	VM     json.Number `json:"vm"`
	Device string      `json:"device"`
}

type Storage struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Path    string   `json:"path"`
	Content []string `json:"content"`
}

type Guest struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Profile      string `json:"profile"`
	ImageVersion string `json:"image_version"`
	Graphics     string `json:"graphics"`

	Cores   *int   `json:"cores"`
	Memory  *int   `json:"memory"`
	Disk    string `json:"disk"`
	Storage string `json:"storage"`
	Bridge  string `json:"bridge"`

	StartOnBoot *bool `json:"start_on_boot"`
	Start       *bool `json:"start"`

	CloudInit *CloudInit `json:"cloudinit"`
	Packages  []string   `json:"packages"`
	Runcmd    []string   `json:"runcmd"`
	// IP — адрес контейнера: «dhcp» или «10.0.0.5/24».
	IP string `json:"ip"`
}

type CloudInit struct {
	User string `json:"user"`
	// SSHKeyFrom: "host" — взять открытые ключи с хоста.
	SSHKeyFrom string `json:"ssh_key_from"`
	// SSHKeyFile — файл с открытыми ключами.
	SSHKeyFile string `json:"ssh_key_file"`
	IPConfig   string `json:"ipconfig"`

	// SSHKeys — уже прочитанные ключи. В манифесте их нет: их наполняет
	// keel, разобрав ssh_key_from и ssh_key_file.
	SSHKeys []string `json:"-"`
}

type Backup struct {
	Schedule string `json:"schedule"`
	// All — копировать всех гостей хоста, а не только перечисленных.
	All      *bool  `json:"all"`
	Storage  string `json:"storage"`
	Mode     string `json:"mode"`
	Guests   []int  `json:"guests"`
	KeepLast *int   `json:"keep_last"`
	Compress string `json:"compress"`
}

// --- Значения по умолчанию ---------------------------------------------------
//
// Ровно те же, что подставлял config_get вторым аргументом. Дефолт здесь
// уточняет уже принятое решение («бэкап делаем»), а не принимает новое:
// без ключа backup весь блок отсутствует и ни один из них не спросят.

func (b *Backup) StorageOr() string  { return orString(b.Storage, "local") }
func (b *Backup) ModeOr() string     { return orString(b.Mode, "snapshot") }
func (b *Backup) CompressOr() string { return orString(b.Compress, "zstd") }
func (b *Backup) KeepLastOr() int    { return orInt(b.KeepLast, 3) }

func (c *ConfigBackup) KeepOr() int        { return orInt(c.Keep, 7) }
func (c *ConfigBackup) MaxAgeHoursOr() int { return orInt(c.MaxAgeHours, 24) }

func (h Host) UpdatesMinSpeedOr() string { return orString(h.UpdatesMinSpeed, "30K") }
func (h Host) UpdatesEnabled() bool      { return h.Updates != nil && *h.Updates }

func (p *GPUPassthrough) DeviceOr() string { return orString(p.Device, "auto") }

func orString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orInt(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

// --- Чтение ------------------------------------------------------------------

// Load читает манифест с диска. Отсутствующий файл — это ошибка: «не трогай
// ничего» выражается пустым {}, а не пропавшим файлом, иначе опечатка в пути
// выглядела бы как «делать нечего».
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", path, err)
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", path, err)
	}
	m.Path = path
	return m, nil
}

// Parse разбирает содержимое манифеста.
func Parse(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(Relax(raw)))
	// Неизвестный ключ — это почти всегда опечатка, и промолчать о ней
	// хуже, чем отказаться: молча проигнорированный ключ выглядит как
	// «keel меня не послушался».
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}
