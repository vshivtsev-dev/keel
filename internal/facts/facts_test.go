package facts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
)

// fakeHost собирает хост из файлов во временном каталоге. Это тот же приём,
// что KEEL_FS_ROOT в bash-версии: факты читаются по-настоящему, но живая
// машина при этом не участвует.
func fakeHost(t *testing.T) (paths.Paths, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("etc/pve/.version", "1\n")
	write("etc/os-release", "ID=debian\nVERSION_CODENAME=trixie\n")
	write("etc/apt/sources.list.d/pve.sources", "Types: deb\n")
	write("etc/default/grub", "GRUB_CMDLINE_LINUX=\"\"\n")
	write("proc/cpuinfo", "vendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 7 8745HS\n")
	write("etc/pve/storage.cfg", `dir: local
	path /var/lib/vz
	content iso,vztmpl,backup

lvmthin: local-lvm
	thinpool data
	vgname pve
	content rootdir,images
`)
	write("etc/pve/qemu-server/100.conf", "name: haos\ncores: 2\n")
	write("etc/pve/lxc/101.conf", "hostname: desktop\ncores: 4\n")

	return paths.NewAt(filepath.Join(root, "home"), root), root
}

func TestCollectReadsStorageConfig(t *testing.T) {
	p, _ := fakeHost(t)
	c := exec.NewFake()
	c.Missing["pveversion"] = true
	c.Missing["lspci"] = true
	c.Missing["ip"] = true
	c.Missing["apt-get"] = true
	c.Missing["proxmox-boot-tool"] = true

	f := Collect(context.Background(), p, c)

	if len(f.Storages) != 2 {
		t.Fatalf("хранилищ %d, ожидалось 2: %+v", len(f.Storages), f.Storages)
	}
	local := f.Storage("local")
	if local == nil {
		t.Fatal("хранилище local не найдено")
	}
	if local.Type != "dir" || local.Path != "/var/lib/vz" {
		t.Errorf("local разобрано неверно: %+v", local)
	}
	// content хранится отсортированным, чтобы сравнение не зависело от
	// порядка, в котором его записал Proxmox.
	if !local.HasContent("iso") || !local.HasContent("backup") || local.HasContent("snippets") {
		t.Errorf("content у local разобран неверно: %v", local.Content)
	}
	if lvm := f.Storage("local-lvm"); lvm == nil || lvm.Type != "lvmthin" {
		t.Errorf("local-lvm разобрано неверно: %+v", lvm)
	}
	if f.Storage("нет-такого") != nil {
		t.Error("несуществующее хранилище найдено")
	}
}

func TestCollectReadsGuestsFromConfigs(t *testing.T) {
	p, _ := fakeHost(t)
	c := exec.NewFake()
	for _, m := range []string{"pveversion", "lspci", "ip", "apt-get", "proxmox-boot-tool"} {
		c.Missing[m] = true
	}

	f := Collect(context.Background(), p, c)

	if len(f.Guests) != 2 {
		t.Fatalf("гостей %d, ожидалось 2: %+v", len(f.Guests), f.Guests)
	}
	if f.Guests[0].ID != 100 || f.Guests[0].Kind != "vm" || f.Guests[0].Name != "haos" {
		t.Errorf("гость 100 разобран неверно: %+v", f.Guests[0])
	}
	if f.Guests[1].ID != 101 || f.Guests[1].Kind != "lxc" || f.Guests[1].Name != "desktop" {
		t.Errorf("гость 101 разобран неверно: %+v", f.Guests[1])
	}
	if !f.GuestExists(100) || f.GuestExists(999) {
		t.Error("GuestExists отвечает неверно")
	}
}

func TestCollectReadsHostBasics(t *testing.T) {
	p, _ := fakeHost(t)
	c := exec.NewFake()
	c.Out["pveversion"] = "pve-manager/9.0.3/abcdef (running kernel: 6.14.0-2-pve)\n"
	c.Out["proxmox-boot-tool status"] = "System currently booted with uefi\n/dev/sda2 is configured with: uefi (systemd-boot)\n"
	c.Missing["lspci"] = true
	c.Missing["ip"] = true
	c.Missing["apt-get"] = true

	f := Collect(context.Background(), p, c)

	if !f.IsPVE {
		t.Error("хост не опознан как Proxmox VE")
	}
	if f.PVEMajor != 9 {
		t.Errorf("мажорная версия %d, ожидалась 9 (из %q)", f.PVEMajor, f.PVEVersion)
	}
	if f.Codename != "trixie" {
		t.Errorf("кодовое имя %q, ожидалось trixie", f.Codename)
	}
	if f.RepoStyle != "deb822" {
		t.Errorf("стиль репозиториев %q, ожидался deb822", f.RepoStyle)
	}
	if f.Bootloader != "systemd-boot (через proxmox-boot-tool)" {
		t.Errorf("загрузчик определён неверно: %q", f.Bootloader)
	}
	if f.CPUVendor != "AMD" || f.CPUModel != "AMD Ryzen 7 8745HS" {
		t.Errorf("процессор разобран неверно: %q %q", f.CPUVendor, f.CPUModel)
	}
	if f.IOMMU {
		t.Error("IOMMU не должен быть включён: каталога /sys/class/iommu нет")
	}
}

func TestCollectCountsUpgradablePackages(t *testing.T) {
	p, _ := fakeHost(t)
	c := exec.NewFake()
	for _, m := range []string{"pveversion", "lspci", "ip", "proxmox-boot-tool"} {
		c.Missing[m] = true
	}
	c.Out["apt-get -s dist-upgrade"] = `Reading package lists...
Inst libc6 [2.41] (2.42 Debian:13/trixie [amd64])
Conf libc6 (2.42 Debian:13/trixie [amd64])
Inst zlib1g [1:1.3] (1:1.4 Debian:13/trixie [amd64])
`
	f := Collect(context.Background(), p, c)
	if f.Upgradable != 2 {
		t.Errorf("к обновлению %d пакетов, ожидалось 2", f.Upgradable)
	}
}

// Отпечаток должен меняться ровно тогда, когда меняется то, от чего зависит
// план. Иначе устаревший план применится как свежий.
func TestDigestReactsToHostChanges(t *testing.T) {
	base := &Facts{Hostname: "pve-01", Upgradable: 0,
		Storages: []Storage{{Name: "local", Type: "dir", Content: []string{"iso"}}}}

	same := &Facts{Hostname: "pve-01", Upgradable: 0,
		Storages: []Storage{{Name: "local", Type: "dir", Content: []string{"iso"}}}}
	if base.Digest() != same.Digest() {
		t.Error("одинаковые факты дали разные отпечатки")
	}

	changed := []*Facts{
		{Hostname: "pve-02", Storages: base.Storages},
		{Hostname: "pve-01", Storages: base.Storages, Upgradable: 3},
		{Hostname: "pve-01", Storages: []Storage{{Name: "local", Type: "dir", Content: []string{"iso", "snippets"}}}},
		{Hostname: "pve-01", Storages: base.Storages, Guests: []Guest{{ID: 100, Kind: "vm"}}},
	}
	for i, f := range changed {
		if f.Digest() == base.Digest() {
			t.Errorf("случай %d: хост изменился, а отпечаток нет", i)
		}
	}
}
