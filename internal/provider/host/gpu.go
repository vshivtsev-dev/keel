package host

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// GPU отдаёт видеокарту хоста виртуальной машине целиком.
//
// Порт modules/host/60-gpu-passthrough.sh — самого опасного модуля keel.
// Если видеокарта на хосте одна, после перезагрузки локальный монитор
// погаснет навсегда: управление останется только через веб-интерфейс и
// SSH, а если ВМ не поднимется — чинить придётся вслепую или с live-USB.
//
// Поэтому здесь всё устроено вокруг отказа: проверки до единого изменения,
// подтверждение с набором адреса устройства, запись обо всех правках и
// откат одной командой.
type GPU struct {
	// Paths нужны, чтобы прочитать нынешнее содержимое файлов ядра: их
	// keel дополняет, а не заменяет.
	Paths paths.Paths
	// Capturer спрашивает у lspci идентификаторы устройства.
	Capturer interface {
		Capture(ctx context.Context, name string, args ...string) (string, error)
		Has(name string) bool
	}
}

func (GPU) ID() string    { return "host/gpu" }
func (GPU) Title() string { return "Проброс видеокарты в ВМ" }

func (GPU) Describe() string {
	return `Отдаёт видеокарту хоста виртуальной машине целиком: параметры ядра, модули
vfio, привязка устройства, hostpci в конфиге ВМ. Каждое изменение
записывается, откат делается одной командой keel gpu revert.
Если видеокарта одна — хост теряет локальный монитор.`
}

func (GPU) Configured(m *manifest.Manifest) bool {
	return m.Host.GPUPassthrough != nil && m.Host.GPUPassthrough.VM.String() != ""
}

// Target — выбранное устройство и всё, что о нём нужно знать.
type Target struct {
	Address string
	IDs     string
	VM      string
	// OnlyCard — других видеокарт на хосте нет, и после перезагрузки
	// монитор погаснет.
	OnlyCard bool
}

// Resolve выбирает устройство и проверяет, что проброс вообще возможен.
//
// «auto» означает «единственная видеокарта на хосте». Если их несколько,
// keel отказывается угадывать: ошибиться здесь — значит отдать в ВМ не ту
// карту и остаться без картинки.
func (g GPU) Resolve(ctx context.Context, m *manifest.Manifest, f *facts.Facts) (*Target, error) {
	cfg := m.Host.GPUPassthrough
	vm := cfg.VM.String()
	if vm == "" {
		return nil, fmt.Errorf("не указано, какой ВМ отдать видеокарту (host.gpu_passthrough.vm)")
	}
	if len(f.GPUs) == 0 {
		return nil, fmt.Errorf("на хосте не найдено ни одной видеокарты")
	}

	addr := cfg.DeviceOr()
	if addr == "auto" {
		if len(f.GPUs) > 1 {
			return nil, fmt.Errorf("видеокарт несколько — выбери явно, какую пробрасывать "+
				"(host.gpu_passthrough.device):\n%s", listGPUs(f))
		}
		addr = f.GPUs[0].Address
	} else if f.GPUByAddress(addr) == nil {
		return nil, fmt.Errorf("устройства %s нет среди видеокарт хоста:\n%s", addr, listGPUs(f))
	}

	ids := f.PCIIDs(ctx, g.Capturer, addr)
	if ids == "" {
		return nil, fmt.Errorf("не удалось определить ID устройства %s", addr)
	}
	return &Target{Address: addr, IDs: ids, VM: vm, OnlyCard: len(f.GPUs) <= 1}, nil
}

func listGPUs(f *facts.Facts) string {
	var b strings.Builder
	for _, g := range f.GPUs {
		fmt.Fprintf(&b, "  %s  %s\n", g.Address, g.Desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Preflight проверяет предпосылки. Лучше отказаться с объяснением, чем
// сделать «как-нибудь» и оставить хост без картинки.
func (g GPU) Preflight(t *Target, f *facts.Facts) (report []string, err error) {
	var problems []string

	if f.IOMMU {
		report = append(report, "IOMMU включён")
	} else {
		problems = append(problems, "IOMMU выключен: включи VT-d/AMD-Vi в BIOS, иначе проброс невозможен")
	}

	group := f.IOMMUGroupOf(g.Paths, t.Address)
	switch {
	case group == "":
		problems = append(problems, "у устройства нет IOMMU-группы — проброс невозможен")
	default:
		members := f.IOMMUGroupMembers(g.Paths, group)
		if len(members) > 1 {
			problems = append(problems, fmt.Sprintf(
				"IOMMU-группа %s делится с другими устройствами: %s.\n"+
					"Они уйдут в ВМ вместе с видеокартой — это почти наверняка не то, что нужно",
				group, strings.Join(members, " ")))
		} else {
			report = append(report, "IOMMU-группа "+group+": устройство изолировано")
		}
	}

	if f.Bootloader == "неизвестно" || f.Bootloader == "" {
		problems = append(problems, "загрузчик не определён — некуда прописывать параметры ядра")
	} else {
		report = append(report, "загрузчик: "+f.Bootloader)
	}

	if len(problems) > 0 {
		return report, fmt.Errorf("предпосылки для проброса не выполнены:\n  %s",
			strings.Join(problems, "\n  "))
	}
	return report, nil
}

func (g GPU) Plan(ctx context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	t, err := g.Resolve(ctx, m, f)
	if err != nil {
		return plan.Changes{}, err
	}
	if _, err := g.Preflight(t, f); err != nil {
		return plan.Changes{}, err
	}

	var out plan.Changes
	if t.OnlyCard {
		out.Notes = append(out.Notes, plan.Note{
			Resource: "видеокарта " + t.Address,
			Message: "это единственная видеокарта хоста.\n" +
				"После перезагрузки локальный монитор погаснет НАВСЕГДА: управление останется\n" +
				"только через веб-интерфейс и SSH. Откат: keel gpu revert",
		})
	}

	var prev string
	add := func(id, summary, path, content string) {
		step := plan.Step{
			ID: "host/gpu:" + id, Provider: g.ID(), Resource: "видеокарта " + t.Address,
			Summary: summary, Action: plan.ActionWrite, Path: path, Content: []byte(content),
		}
		if prev != "" {
			step.Needs = []string{prev}
		}
		out.Steps = append(out.Steps, step)
		prev = step.ID
	}

	cmdlineFile := f.CmdlineFile()
	if want := g.cmdlineContent(f, cmdlineFile); want != g.read(cmdlineFile) {
		add("cmdline", "добавить параметры ядра ("+strings.Join(f.KernelIOMMUParams(), " ")+")",
			cmdlineFile, want)
	}

	if want := g.modulesContent(); want != g.read("/etc/modules") {
		add("modules", "включить модули vfio", "/etc/modules", want)
	}

	if want := modprobeContent(t); want != g.read("/etc/modprobe.d/keel-vfio.conf") {
		add("modprobe", "привязать "+t.Address+" к vfio-pci",
			"/etc/modprobe.d/keel-vfio.conf", want)
	}

	// Ничего не меняется — значит всё уже сделано, и трогать загрузчик
	// незачем: update-initramfs на ровном месте занимает минуту.
	if len(out.Steps) == 0 && g.hostpciSet(f, t) {
		return out, nil
	}

	if len(out.Steps) > 0 {
		refresh := []string{"proxmox-boot-tool", "refresh"}
		if f.UsesGRUB() {
			refresh = []string{"update-grub"}
		}
		bootID := "host/gpu:boot"
		out.Steps = append(out.Steps, plan.Step{
			ID: bootID, Provider: g.ID(), Resource: "загрузчик",
			Summary: "обновить конфигурацию загрузчика",
			Action:  plan.ActionExec, Cmd: refresh, Needs: []string{prev},
		})
		out.Steps = append(out.Steps, plan.Step{
			ID: "host/gpu:initramfs", Provider: g.ID(), Resource: "initramfs",
			Summary: "пересобрать initramfs",
			Action:  plan.ActionExec, Cmd: []string{"update-initramfs", "-u", "-k", "all"},
			Needs: []string{bootID},
		})
		prev = "host/gpu:initramfs"
	}

	switch {
	case !f.GuestExists(vmID(t.VM)):
		out.Notes = append(out.Notes, plan.Note{
			Resource: "ВМ " + t.VM,
			Message: "ВМ ещё нет — видеокарту отдадим после её создания.\n" +
				"Создай гостя (keel apply) и повтори: keel apply --only host/gpu",
		})
	case !g.hostpciSet(f, t):
		out.Steps = append(out.Steps, plan.Step{
			ID: "host/gpu:hostpci", Provider: g.ID(), Resource: "ВМ " + t.VM,
			Summary: "отдать видеокарту ВМ " + t.VM,
			Action:  plan.ActionExec,
			Cmd:     []string{"qm", "set", t.VM, "--hostpci0", t.Address + ",pcie=1"},
			Needs:   nonEmpty(prev),
		})
	}
	return out, nil
}

func (g GPU) Verify(ctx context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	t, err := g.Resolve(ctx, m, f)
	if err != nil {
		return nil, err
	}
	card := f.GPUByAddress(t.Address)
	driver := "нет"
	if card != nil {
		driver = card.Driver
	}
	if driver == "vfio-pci" {
		return []plan.Finding{{Provider: g.ID(), Resource: "видеокарта " + t.Address,
			Message: "держит vfio-pci — устройство готово к пробросу", OK: true}}, nil
	}
	return []plan.Finding{{Provider: g.ID(), Resource: "видеокарта " + t.Address,
		Message: fmt.Sprintf("пока держит драйвер «%s», а нужен vfio-pci "+
			"(если изменения уже применены — хост ещё не перезагружен)", driver)}}, nil
}

// --- Содержимое файлов -------------------------------------------------------

// cmdlineContent добавляет параметры к существующим, а не заменяет их:
// в cmdline хоста уже может стоять что-то нужное.
func (g GPU) cmdlineContent(f *facts.Facts, file string) string {
	params := f.KernelIOMMUParams()
	current := g.read(file)

	if !f.UsesGRUB() {
		line := strings.TrimSpace(firstLine(current))
		for _, p := range params {
			if !hasWord(line, p) {
				line = strings.TrimSpace(line + " " + p)
			}
		}
		return line + "\n"
	}

	const key = "GRUB_CMDLINE_LINUX_DEFAULT="
	var out []string
	seen := false
	for _, line := range strings.Split(strings.TrimRight(current, "\n"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), key) {
			out = append(out, line)
			continue
		}
		seen = true
		inner := ""
		if a := strings.Index(line, `"`); a >= 0 {
			if b := strings.LastIndex(line, `"`); b > a {
				inner = line[a+1 : b]
			}
		}
		for _, p := range params {
			if !hasWord(inner, p) {
				inner = strings.TrimSpace(inner + " " + p)
			}
		}
		out = append(out, key+`"`+inner+`"`)
	}
	if !seen {
		out = append(out, key+`"`+strings.Join(params, " ")+`"`)
	}
	return strings.Join(out, "\n") + "\n"
}

func (g GPU) modulesContent() string {
	current := g.read("/etc/modules")
	out := strings.TrimRight(current, "\n")
	for _, m := range []string{"vfio", "vfio_iommu_type1", "vfio_pci"} {
		if !hasLine(current, m) {
			if out != "" {
				out += "\n"
			}
			out += m
		}
	}
	return out + "\n"
}

func modprobeContent(t *Target) string {
	return fmt.Sprintf(`# Создано keel. Видеокарта %s (%s) отдана ВМ %s.
#
# Откатить: keel gpu revert
# Если хост не загрузился — удалить этот файл с live-USB и обновить initramfs.

options vfio-pci ids=%s disable_vga=1

# vfio-pci должен успеть забрать устройство раньше штатного драйвера
softdep amdgpu pre: vfio-pci
softdep radeon pre: vfio-pci
softdep nouveau pre: vfio-pci
softdep nvidia pre: vfio-pci

blacklist amdgpu
blacklist radeon
`, t.Address, t.IDs, t.VM, t.IDs)
}

// hostpciSet — отдана ли карта этой ВМ уже сейчас.
func (g GPU) hostpciSet(f *facts.Facts, t *Target) bool {
	return strings.Contains(f.GuestField(vmID(t.VM), "hostpci0"), t.Address)
}

func (g GPU) read(path string) string {
	raw, err := os.ReadFile(g.Paths.Sys(path))
	if err != nil {
		return ""
	}
	return string(raw)
}

func vmID(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func hasWord(line, word string) bool {
	return strings.Contains(" "+line+" ", " "+word+" ")
}

func hasLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
