package facts

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
)

// IOMMUGroupOf — в какой группе сидит устройство. Пусто — группы нет,
// а значит проброс невозможен.
func (f *Facts) IOMMUGroupOf(p paths.Paths, addr string) string {
	for _, dev := range glob(p.Sys("/sys/kernel/iommu_groups/*/devices/*")) {
		if strings.HasSuffix(dev, addr) {
			// .../iommu_groups/12/devices/0000:64:00.0 → 12
			group := filepath.Dir(filepath.Dir(dev))
			return filepath.Base(group)
		}
	}
	return ""
}

// IOMMUGroupMembers — кто ещё сидит в той же группе. От этого зависит,
// можно ли пробросить карту отдельно: соседи уедут в ВМ вместе с ней.
func (f *Facts) IOMMUGroupMembers(p paths.Paths, group string) []string {
	var out []string
	for _, dev := range glob(p.Sys("/sys/kernel/iommu_groups/" + group + "/devices/*")) {
		out = append(out, filepath.Base(dev))
	}
	return out
}

// PCIIDs — vendor:device устройства. Именно по ним vfio-pci забирает
// устройство себе.
func (f *Facts) PCIIDs(ctx context.Context, c exec.Capturer, addr string) string {
	if !c.Has("lspci") {
		return ""
	}
	out, err := c.Capture(ctx, "lspci", "-n", "-s", addr)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			return fields[2]
		}
	}
	return ""
}

// GPUByAddress находит видеокарту по PCI-адресу.
func (f *Facts) GPUByAddress(addr string) *GPU {
	for i := range f.GPUs {
		if f.GPUs[i].Address == addr {
			return &f.GPUs[i]
		}
	}
	return nil
}

// UsesGRUB — куда писать параметры ядра. На PVE с proxmox-boot-tool правка
// /etc/default/grub не делает ничего, и это самая частая причина «сделал
// всё по гайду, не завелось».
func (f *Facts) UsesGRUB() bool {
	return !strings.Contains(f.Bootloader, "proxmox-boot-tool")
}

// CmdlineFile — файл с параметрами ядра для этого хоста.
func (f *Facts) CmdlineFile() string {
	if f.UsesGRUB() {
		return "/etc/default/grub"
	}
	return "/etc/kernel/cmdline"
}

// KernelIOMMUParams — параметры ядра под производителя процессора.
func (f *Facts) KernelIOMMUParams() []string {
	if f.CPUVendor == "AMD" {
		return []string{"amd_iommu=on", "iommu=pt"}
	}
	return []string{"intel_iommu=on", "iommu=pt"}
}
