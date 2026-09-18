// Package facts собирает то, что keel знает о хосте. Только чтение:
// ничего в этом пакете систему не меняет, поэтому его свободно вызывают
// и сборка плана, и проверка, и doctor.
//
// Там, где можно прочитать файл вместо запуска команды, читается файл:
// так факты собираются и на хосте с остановленными службами, и в тестах
// через KEEL_FS_ROOT.
package facts

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

type Facts struct {
	Hostname string

	IsPVE      bool
	PVEVersion string
	PVEMajor   int
	Codename   string
	RepoStyle  string // deb822 | list
	Bootloader string

	CPUVendor string
	CPUModel  string
	IOMMU     bool

	GPUs     []GPU
	DRINodes []string

	Bridges    []string
	Storages   []Storage
	Guests     []Guest
	AptSources []AptSource
	BackupJobs []BackupJob
	// Keyring — ключ, которым подписаны пакеты Proxmox. Путь зависит от
	// версии PVE, и угадывать его нельзя: не тот ключ — apt отвергнет
	// репозиторий целиком.
	Keyring string

	Upgradable int
	// Upgradables — имена пакетов, ждущих обновления.
	Upgradables []string
	// AptError — жалобы apt на списки источников. Нужны потому, что при
	// битых источниках apt-get -s ничего не печатает, и «ноль обновлений»
	// неотличим от «обновлять нечего».
	AptError string
	// UpgradeURI — первая ссылка из тех, что apt собирается скачать.
	// Лучшая проба связи: файл точно существует и лежит ровно там, куда
	// пойдёт обновление, — в отличие от любого выдуманного адреса.
	UpgradeURI string
	// UpgradeBytes — сколько всего предстоит скачать. Нужно, чтобы сказать
	// человеку не «медленно», а «на такой скорости это займёт полтора часа».
	UpgradeBytes int64
	// RebootRequired — система просит перезагрузку. keel её не делает.
	RebootRequired bool

	// ConfigArchives — уже сделанные копии конфигурации, свежая первой.
	ConfigArchives []Archive

	// SysForPaths — как отображаются системные пути. Держится в фактах,
	// чтобы провайдер мог спросить «а что из этого есть на хосте», не
	// зная про песочницу и не трогая файловую систему сам.
	SysForPaths func(string) string `json:"-"`

	// Report — текстовый снимок хоста: то, чего нет в конфигах, но что
	// очень нужно знать при сборке машины заново. Кладётся в архив
	// конфигурации рядом с файлами.
	Report string
}

type GPU struct {
	Address string
	Desc    string
	Driver  string
}

type Storage struct {
	Name    string
	Type    string
	Path    string
	Content []string
}

func (s Storage) HasContent(t string) bool {
	for _, c := range s.Content {
		if c == t {
			return true
		}
	}
	return false
}

type Guest struct {
	ID   int
	Kind string // vm | lxc
	Name string
}

// Collect собирает факты о хосте. Ошибка отдельного источника не обрывает
// сбор: неизвестный факт — это «не знаю», а не повод остаться без отчёта
// на полумёртвой системе, ради которой keel и существует.
func Collect(ctx context.Context, p paths.Paths, c exec.Capturer) *Facts {
	f := &Facts{}
	f.Hostname, _ = os.Hostname()

	f.collectPVE(ctx, p, c)
	f.collectCPU(p)
	f.collectGPU(ctx, p, c)
	f.collectStorages(p)
	f.collectGuests(p)
	f.collectBridges(ctx, c)
	f.collectAPT(ctx, p, c)
	f.collectAptSources(p.Sys)
	f.collectBackupJobs(ctx, c)
	f.Report = buildReport(ctx, c)
	f.SysForPaths = p.Sys
	f.Keyring = findKeyring(p, f.Codename)
	return f
}

func (f *Facts) collectPVE(ctx context.Context, p paths.Paths, c exec.Capturer) {
	_, err := os.Stat(p.Sys("/etc/pve/.version"))
	f.IsPVE = err == nil || c.Has("pveversion")

	if c.Has("pveversion") {
		if out, err := c.Capture(ctx, "pveversion"); err == nil {
			f.PVEVersion = firstLine(out)
			f.PVEMajor = pveMajor(f.PVEVersion)
		}
	}
	f.Codename = osReleaseField(p.Sys("/etc/os-release"), "VERSION_CODENAME")
	f.RepoStyle = repoStyle(p, f.PVEMajor)
	f.Bootloader = bootloader(ctx, p, c)
}

// pveMajor достаёт мажорную версию из строки вида "pve-manager/9.0.3/...".
func pveMajor(line string) int {
	i := strings.Index(line, "pve-manager/")
	if i < 0 {
		return 0
	}
	rest := line[i+len("pve-manager/"):]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:dot])
	if err != nil {
		return 0
	}
	return n
}

// repoStyle: deb822 (*.sources, PVE 9 / Debian 13) или list (*.list, PVE 8).
func repoStyle(p paths.Paths, major int) string {
	dir := p.Sys("/etc/apt/sources.list.d")
	if globAny(filepath.Join(dir, "*.sources")) {
		return "deb822"
	}
	if globAny(filepath.Join(dir, "*.list")) {
		return "list"
	}
	if major >= 9 {
		return "deb822"
	}
	return "list"
}

// bootloader важен для проброса видеокарты: на ZFS хост грузится
// systemd-boot, и правка /etc/default/grub там не делает ничего.
func bootloader(ctx context.Context, p paths.Paths, c exec.Capturer) string {
	if c.Has("proxmox-boot-tool") {
		if out, err := c.Capture(ctx, "proxmox-boot-tool", "status"); err == nil {
			if strings.Contains(strings.ToLower(out), "systemd-boot") {
				return "systemd-boot (через proxmox-boot-tool)"
			}
			return "grub (через proxmox-boot-tool)"
		}
	}
	if _, err := os.Stat(p.Sys("/etc/default/grub")); err == nil {
		return "grub"
	}
	return "неизвестно"
}

// findKeyring ищет ключ Proxmox среди известных имён. Не нашли — берём
// нынешнее: на свежем PVE 9 оно верное, а на чужом хосте ключа нет вовсе
// и подставлять нечего.
func findKeyring(p paths.Paths, codename string) string {
	candidates := []string{"/usr/share/keyrings/proxmox-archive-keyring.gpg"}
	if codename != "" {
		candidates = append(candidates, "/usr/share/keyrings/proxmox-release-"+codename+".gpg")
	}
	for _, c := range candidates {
		if _, err := os.Stat(p.Sys(c)); err == nil {
			return c
		}
	}
	return candidates[0]
}

func (f *Facts) collectCPU(p paths.Paths) {
	raw, err := os.ReadFile(p.Sys("/proc/cpuinfo"))
	if err == nil {
		text := string(raw)
		switch {
		case strings.Contains(text, "AuthenticAMD"):
			f.CPUVendor = "AMD"
		case strings.Contains(text, "GenuineIntel"):
			f.CPUVendor = "Intel"
		default:
			f.CPUVendor = "неизвестно"
		}
		f.CPUModel = fieldAfterColon(text, "model name")
	}
	f.IOMMU = globAny(filepath.Join(p.Sys("/sys/class/iommu"), "*"))
}

func (f *Facts) collectGPU(ctx context.Context, p paths.Paths, c exec.Capturer) {
	if c.Has("lspci") {
		out, err := c.Capture(ctx, "lspci", "-mm")
		if err == nil {
			for _, line := range strings.Split(out, "\n") {
				if !isDisplayDevice(line) {
					continue
				}
				addr, desc := splitAddr(strings.ReplaceAll(line, `"`, ""))
				if addr == "" {
					continue
				}
				g := GPU{Address: addr, Desc: desc, Driver: "нет"}
				if kout, err := c.Capture(ctx, "lspci", "-k", "-s", addr); err == nil {
					if drv := fieldAfterColon(kout, "Kernel driver in use"); drv != "" {
						g.Driver = drv
					}
				}
				f.GPUs = append(f.GPUs, g)
			}
		}
	}
	// by-path и by-id — каталоги со ссылками, устройствами они не являются.
	for _, n := range glob(filepath.Join(p.Sys("/dev/dri"), "*")) {
		if st, err := os.Stat(n); err == nil && st.IsDir() {
			continue
		}
		f.DRINodes = append(f.DRINodes, n)
	}
}

func isDisplayDevice(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "vga compatible") ||
		strings.Contains(l, "display controller") ||
		strings.Contains(l, "3d controller")
}

func splitAddr(line string) (addr, desc string) {
	line = strings.TrimSpace(line)
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return "", ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// collectStorages читает /etc/pve/storage.cfg. Формат: строка «тип: имя»,
// под ней поля с отступом.
func (f *Facts) collectStorages(p paths.Paths) {
	file, err := os.Open(p.Sys("/etc/pve/storage.cfg"))
	if err != nil {
		return
	}
	defer file.Close()

	var cur *Storage
	flush := func() {
		if cur != nil {
			f.Storages = append(f.Storages, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			flush()
			typ, name, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			cur = &Storage{Type: strings.TrimSpace(typ), Name: strings.TrimSpace(name)}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "path":
			cur.Path = val
		case "content":
			cur.Content = splitSorted(val)
		}
	}
	flush()
}

// collectGuests читает конфиги гостей, а не спрашивает qm/pct: так список
// собирается и когда службы Proxmox не подняты.
func (f *Facts) collectGuests(p paths.Paths) {
	for _, src := range []struct {
		dir, kind, nameKey string
	}{
		{p.Sys("/etc/pve/qemu-server"), "vm", "name"},
		{p.Sys("/etc/pve/lxc"), "lxc", "hostname"},
	} {
		for _, conf := range glob(filepath.Join(src.dir, "*.conf")) {
			base := strings.TrimSuffix(filepath.Base(conf), ".conf")
			id, err := strconv.Atoi(base)
			if err != nil {
				continue
			}
			g := Guest{ID: id, Kind: src.kind}
			if raw, err := os.ReadFile(conf); err == nil {
				g.Name = fieldAfterColon(string(raw), src.nameKey)
			}
			f.Guests = append(f.Guests, g)
		}
	}
	sort.Slice(f.Guests, func(i, j int) bool { return f.Guests[i].ID < f.Guests[j].ID })
}

func (f *Facts) collectBridges(ctx context.Context, c exec.Capturer) {
	if !c.Has("ip") {
		return
	}
	out, err := c.Capture(ctx, "ip", "-o", "link", "show", "type", "bridge")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		_, rest, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if name, _, ok := strings.Cut(rest, ":"); ok {
			f.Bridges = append(f.Bridges, strings.TrimSpace(name))
		}
	}
}

// collectAPT только моделирует обновление (-s) и ничего не ставит.
func (f *Facts) collectAPT(ctx context.Context, p paths.Paths, c exec.Capturer) {
	if _, err := os.Stat(p.Sys("/var/run/reboot-required")); err == nil {
		f.RebootRequired = true
	}
	if !c.Has("apt-get") {
		return
	}

	out, err := c.Capture(ctx, "apt-get", "-s", "dist-upgrade")
	if err != nil {
		// apt печатает жалобы на источники в stderr, а на stdout молчит —
		// поэтому «ноль обновлений» и «apt не смог» надо различать явно.
		f.AptError = firstErrorLines(err.Error())
		return
	}
	for _, line := range strings.Split(out, "\n") {
		name, ok := strings.CutPrefix(line, "Inst ")
		if !ok {
			continue
		}
		f.Upgradable++
		if i := strings.IndexByte(name, ' '); i > 0 {
			name = name[:i]
		}
		f.Upgradables = append(f.Upgradables, name)
	}

	if f.Upgradable == 0 {
		return
	}
	if uris, err := c.Capture(ctx, "apt-get", "--print-uris", "-qq", "-y", "dist-upgrade"); err == nil {
		f.UpgradeURI, f.UpgradeBytes = parseUpgradeURIs(uris)
	}
}

// parseUpgradeURIs разбирает строки вида: 'ссылка' имя размер MD5Sum:…
func parseUpgradeURIs(out string) (first string, total int64) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		uri := strings.Trim(fields[0], "'")
		if first == "" {
			first = uri
		}
		if n, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			total += n
		}
	}
	return first, total
}

func firstErrorLines(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "E:") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	if len(out) == 0 {
		return strings.TrimSpace(text)
	}
	return strings.Join(out, "\n")
}

// Archive — копия конфигурации хоста, лежащая на диске.
type Archive struct {
	Path string
	Age  time.Duration
}

// ConfigArchivesIn читает каталог с копиями конфигурации, свежая первой.
// Вызывается отдельно: путь к нему задаётся манифестом, а не известен
// заранее, как прочие места на хосте.
func (f *Facts) ConfigArchivesIn(sys func(string) string, dir string) {
	f.ConfigArchives = nil
	if dir == "" {
		return
	}
	now := time.Now()
	for _, path := range glob(filepath.Join(sys(dir), "keel-host-*.tar.gz")) {
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		f.ConfigArchives = append(f.ConfigArchives, Archive{
			Path: filepath.Join(dir, filepath.Base(path)),
			Age:  now.Sub(st.ModTime()),
		})
	}
	sort.Slice(f.ConfigArchives, func(i, j int) bool {
		return f.ConfigArchives[i].Age < f.ConfigArchives[j].Age
	})
}

// ConfigPaths — что именно кладётся в архив. Пути относительно корня,
// несуществующие пропускаются молча: на разных хостах набор разный.
func (f *Facts) ConfigPaths(sys func(string) string) []string {
	if sys == nil {
		sys = func(s string) string { return s }
	}
	candidates := []string{
		"etc/pve",
		"etc/network/interfaces", "etc/network/interfaces.d",
		"etc/hosts", "etc/hostname", "etc/resolv.conf", "etc/fstab",
		"etc/apt/sources.list", "etc/apt/sources.list.d",
		"etc/default/grub", "etc/kernel/cmdline",
		"etc/modules", "etc/modprobe.d",
		"etc/vzdump.conf", "etc/ssh/sshd_config", "etc/ssh/sshd_config.d",
		"root/.ssh/authorized_keys",
	}
	var out []string
	for _, p := range candidates {
		if _, err := os.Stat(sys("/" + p)); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// Storage находит хранилище по имени.
func (f *Facts) Storage(name string) *Storage {
	for i := range f.Storages {
		if f.Storages[i].Name == name {
			return &f.Storages[i]
		}
	}
	return nil
}

// GuestExists — есть ли на хосте гость с таким id. Гостя, который есть,
// keel не трогает никогда.
func (f *Facts) GuestExists(id int) bool {
	for _, g := range f.Guests {
		if g.ID == id {
			return true
		}
	}
	return false
}

// Digest — отпечаток тех фактов, от которых зависит план. Если он разошёлся,
// значит хост изменился и сохранённый план устарел.
func (f *Facts) Digest() string {
	var parts []string
	parts = append(parts, f.Hostname, f.PVEVersion, f.RepoStyle, f.Bootloader,
		strconv.FormatBool(f.IOMMU), strconv.Itoa(f.Upgradable),
		strconv.FormatBool(f.RebootRequired))
	for _, s := range f.Storages {
		parts = append(parts, "storage:"+s.Name+":"+s.Type+":"+strings.Join(s.Content, ","))
	}
	for _, g := range f.Guests {
		parts = append(parts, "guest:"+strconv.Itoa(g.ID)+":"+g.Kind)
	}
	for _, g := range f.GPUs {
		parts = append(parts, "gpu:"+g.Address+":"+g.Driver)
	}
	for _, b := range f.BackupJobs {
		parts = append(parts, "backup:"+b.ID+":"+b.Comment+":"+b.Schedule+":"+b.Storage+
			":"+b.Mode+":"+b.VMID+":"+strconv.FormatBool(b.All.Bool()))
	}
	for _, a := range f.AptSources {
		parts = append(parts, "apt:"+a.Path+":"+strings.Join(a.Components, ",")+":"+
			strconv.FormatBool(a.Disabled))
	}
	return plan.Digest(parts...)
}

// --- мелкая помощь -----------------------------------------------------------

func splitSorted(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// fieldAfterColon берёт значение первой строки вида «ключ: значение».
func fieldAfterColon(text, key string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, key)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if v, ok := strings.CutPrefix(rest, ":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func osReleaseField(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func glob(pattern string) []string {
	m, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(m)
	return m
}

func globAny(pattern string) bool { return len(glob(pattern)) > 0 }
