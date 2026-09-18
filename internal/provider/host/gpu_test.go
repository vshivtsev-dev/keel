package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

const gpuManifest = `{"host":{"gpu_passthrough":{"vm":201,"device":"auto"}}}`

// gpuHost собирает хост с одной видеокартой в своей IOMMU-группе — тот
// самый случай, ради которого модуль и писался.
func gpuHost(t *testing.T, opts ...func(*facts.Facts, string)) (*facts.Facts, paths.Paths) {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Устройство одно в группе 12.
	mk("sys/kernel/iommu_groups/12/devices/0000:64:00.0", "")
	mk("etc/kernel/cmdline", "root=ZFS=rpool/ROOT/pve-1 boot=zfs\n")
	mk("etc/modules", "# /etc/modules\nloop\n")

	f := &facts.Facts{
		Hostname:   "pve-01",
		CPUVendor:  "AMD",
		IOMMU:      true,
		Bootloader: "systemd-boot (через proxmox-boot-tool)",
		GPUs:       []facts.GPU{{Address: "0000:64:00.0", Desc: "AMD Radeon 780M", Driver: "amdgpu"}},
	}
	p := paths.NewAt(filepath.Join(root, "home"), root)
	f.SysForPaths = p.Sys
	for _, o := range opts {
		o(f, root)
	}
	return f, p
}

func gpuProvider(p paths.Paths) GPU {
	c := exec.NewFake()
	c.Out["lspci -n -s 0000:64:00.0"] = "64:00.0 0300: 1002:15bf (rev c8)\n"
	c.Out["lspci -n -s 0000:65:00.0"] = "65:00.0 0300: 10de:2504 (rev a1)\n"
	return GPU{Paths: p, Capturer: c}
}

func gpuPlan(t *testing.T, body string, f *facts.Facts, p paths.Paths) plan.Changes {
	t.Helper()
	c, err := gpuProvider(p).Plan(context.Background(), parse(t, body), f)
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func TestGPUWritesKernelAndModprobeFiles(t *testing.T) {
	f, p := gpuHost(t)
	c := gpuPlan(t, gpuManifest, f, p)

	byPath := map[string]plan.Step{}
	for _, s := range c.Steps {
		if s.Action == plan.ActionWrite {
			byPath[s.Path] = s
		}
	}

	// Хост грузится через proxmox-boot-tool — значит параметры ядра идут
	// в /etc/kernel/cmdline. Правка /etc/default/grub здесь не делает
	// ничего, и это самая частая причина «сделал всё по гайду, не завелось».
	cmdline, ok := byPath["/etc/kernel/cmdline"]
	if !ok {
		t.Fatalf("параметры ядра пишутся не туда: %v", byPath)
	}
	body := string(cmdline.Content)
	if !strings.Contains(body, "amd_iommu=on") || !strings.Contains(body, "iommu=pt") {
		t.Errorf("параметры ядра собраны неверно: %q", body)
	}
	// Существующие параметры должны остаться: в cmdline уже стоит то,
	// без чего хост не загрузится.
	if !strings.Contains(body, "root=ZFS=rpool/ROOT/pve-1") {
		t.Errorf("затёрты существующие параметры ядра: %q", body)
	}

	modprobe, ok := byPath["/etc/modprobe.d/keel-vfio.conf"]
	if !ok {
		t.Fatal("нет привязки устройства к vfio-pci")
	}
	mbody := string(modprobe.Content)
	for _, want := range []string{
		"options vfio-pci ids=1002:15bf disable_vga=1",
		"softdep amdgpu pre: vfio-pci",
		"blacklist amdgpu",
		"keel gpu revert",
	} {
		if !strings.Contains(mbody, want) {
			t.Errorf("в привязке нет %q:\n%s", want, mbody)
		}
	}

	modules, ok := byPath["/etc/modules"]
	if !ok {
		t.Fatal("модули vfio не включаются")
	}
	for _, want := range []string{"vfio", "vfio_iommu_type1", "vfio_pci", "loop"} {
		if !strings.Contains(string(modules.Content), want) {
			t.Errorf("в /etc/modules нет %q:\n%s", want, modules.Content)
		}
	}
}

func TestGPUUsesRightBootloaderCommand(t *testing.T) {
	f, p := gpuHost(t)
	c := gpuPlan(t, gpuManifest, f, p)
	found := false
	for _, cmd := range commands(c.Steps) {
		if strings.HasPrefix(cmd, "proxmox-boot-tool refresh") {
			found = true
		}
		if strings.HasPrefix(cmd, "update-grub") {
			t.Errorf("на хосте с proxmox-boot-tool вызывается update-grub: %s", cmd)
		}
	}
	if !found {
		t.Errorf("загрузчик не обновляется: %v", commands(c.Steps))
	}

	// А на обычном grub — наоборот.
	f2, p2 := gpuHost(t)
	f2.Bootloader = "grub"
	c2 := gpuPlan(t, gpuManifest, f2, p2)
	var cmdlinePath string
	for _, s := range c2.Steps {
		if s.Action == plan.ActionWrite && strings.Contains(s.Path, "grub") {
			cmdlinePath = s.Path
			if !strings.Contains(string(s.Content), `GRUB_CMDLINE_LINUX_DEFAULT="`) {
				t.Errorf("параметры grub собраны неверно: %q", s.Content)
			}
		}
	}
	if cmdlinePath == "" {
		t.Errorf("на grub параметры пишутся не в /etc/default/grub: %v", commands(c2.Steps))
	}
}

// Лучше отказаться с объяснением, чем сделать «как-нибудь» и оставить
// хост без картинки.
func TestGPURefusesWithoutIOMMU(t *testing.T) {
	f, p := gpuHost(t)
	f.IOMMU = false

	_, err := gpuProvider(p).Plan(context.Background(), parse(t, gpuManifest), f)
	if err == nil {
		t.Fatal("проброс собрался при выключенном IOMMU")
	}
	if !strings.Contains(err.Error(), "BIOS") {
		t.Errorf("ошибка не подсказывает, что делать: %v", err)
	}
}

// Соседи по группе уедут в ВМ вместе с видеокартой — это почти наверняка
// не то, что нужно.
func TestGPURefusesOnSharedIOMMUGroup(t *testing.T) {
	f, p := gpuHost(t, func(_ *facts.Facts, root string) {
		neighbour := filepath.Join(root, "sys/kernel/iommu_groups/12/devices/0000:64:00.1")
		if err := os.WriteFile(neighbour, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	})

	_, err := gpuProvider(p).Plan(context.Background(), parse(t, gpuManifest), f)
	if err == nil {
		t.Fatal("проброс собрался при общей IOMMU-группе")
	}
	if !strings.Contains(err.Error(), "группа") {
		t.Errorf("ошибка не объясняет причину: %v", err)
	}
}

// Ошибиться здесь — значит отдать в ВМ не ту карту и остаться без картинки.
func TestGPURefusesToGuessBetweenCards(t *testing.T) {
	f, p := gpuHost(t)
	f.GPUs = append(f.GPUs, facts.GPU{Address: "0000:65:00.0", Desc: "NVIDIA RTX", Driver: "nouveau"})

	_, err := gpuProvider(p).Plan(context.Background(), parse(t, gpuManifest), f)
	if err == nil {
		t.Fatal("keel сам выбрал одну из двух видеокарт")
	}
	if !strings.Contains(err.Error(), "0000:65:00.0") {
		t.Errorf("ошибка не перечисляет карты: %v", err)
	}
}

// Одна карта — хост потеряет монитор, и молчать об этом нельзя.
func TestGPUWarnsAboutLosingMonitor(t *testing.T) {
	f, p := gpuHost(t)
	c := gpuPlan(t, gpuManifest, f, p)

	var warned bool
	for _, n := range c.Notes {
		if strings.Contains(n.Message, "НАВСЕГДА") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("о потере монитора не сказано: %+v", c.Notes)
	}
}

// Двух карт достаточно, чтобы хост остался с картинкой, — пугать незачем.
func TestGPUSilentWhenSecondCardStays(t *testing.T) {
	f, p := gpuHost(t)
	f.GPUs = append(f.GPUs, facts.GPU{Address: "0000:65:00.0", Desc: "NVIDIA RTX", Driver: "nouveau"})

	c, err := gpuProvider(p).Plan(context.Background(),
		parse(t, `{"host":{"gpu_passthrough":{"vm":201,"device":"0000:64:00.0"}}}`), f)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range c.Notes {
		if strings.Contains(n.Message, "НАВСЕГДА") {
			t.Errorf("пугает потерей монитора при двух картах: %s", n.Message)
		}
	}
}

// Отдавать карту несуществующей ВМ нечему — но и молчать об этом нельзя.
func TestGPUTellsWhenVMIsMissing(t *testing.T) {
	f, p := gpuHost(t)
	c := gpuPlan(t, gpuManifest, f, p)

	for _, cmd := range commands(c.Steps) {
		if strings.Contains(cmd, "hostpci0") {
			t.Errorf("видеокарта отдаётся несуществующей ВМ: %s", cmd)
		}
	}
	var told bool
	for _, n := range c.Notes {
		if strings.Contains(n.Message, "ВМ ещё нет") {
			told = true
		}
	}
	if !told {
		t.Errorf("об отсутствующей ВМ не сказано: %+v", c.Notes)
	}
}

func TestGPURuleOfZero(t *testing.T) {
	if (GPU{}).Configured(parse(t, `{"host":{}}`)) {
		t.Error("без ключа gpu_passthrough провайдер объявил себя настроенным")
	}
}
