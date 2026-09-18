package facts

import (
	"context"
	"regexp"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/exec"
)

// collectLXC узнаёт всё, что нужно для создания контейнеров: какие
// шаблоны уже скачаны, какие доступны, и умеет ли этот Proxmox отдавать
// устройства ключами dev0.
func (f *Facts) collectLXC(ctx context.Context, c exec.Capturer) {
	if !c.Has("pveam") {
		return
	}
	if out, err := c.Capture(ctx, "pveam", "list", "local"); err == nil {
		f.TemplatesDownloaded = templateNames(out)
	}
	if out, err := c.Capture(ctx, "pveam", "available"); err == nil {
		f.TemplatesAvailable = templateNames(out)
	}

	// Ключи dev0..devN появились в PVE 8.2. На более старых версиях
	// видеокарта отдаётся контейнеру правкой конфига руками, и делать
	// вид, что команда сработает, нельзя.
	if c.Has("pct") {
		if out, err := c.Capture(ctx, "pct", "help", "set"); err == nil {
			f.PctDevKeys = strings.Contains(out, "dev[n]") || strings.Contains(out, "--dev0")
		}
	}
}

// templateNames достаёт имена шаблонов из вывода pveam. Строки выглядят
// как «system  ubuntu-24.04-standard_24.04-2_amd64.tar.zst  123MB» или
// как путь «local:vztmpl/…» — берём последний кусок пути.
func templateNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			if !strings.HasSuffix(field, ".tar.zst") && !strings.HasSuffix(field, ".tar.gz") &&
				!strings.HasSuffix(field, ".tar.xz") {
				continue
			}
			names = append(names, field[strings.LastIndexByte(field, '/')+1:])
		}
	}
	return names
}

// Template ищет шаблон по образцу. Уже скачанный важнее доступного:
// скачивать заново то, что лежит на диске, — минуты ожидания на ровном
// месте. Из подходящих берётся последний: имена шаблонов упорядочены по
// версии, и самый свежий стоит в конце.
func (f *Facts) Template(pattern string) (name string, downloaded bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	if got := lastMatch(re, f.TemplatesDownloaded); got != "" {
		return got, true
	}
	return lastMatch(re, f.TemplatesAvailable), false
}

func lastMatch(re *regexp.Regexp, names []string) string {
	out := ""
	for _, n := range names {
		if re.MatchString(n) {
			out = n
		}
	}
	return out
}

// SnippetStorage — хранилище, которому разрешены сниппеты. Без них
// cloud-init не настроить: именно туда кладётся user-data.
func (f *Facts) SnippetStorage() *Storage {
	for i := range f.Storages {
		if f.Storages[i].HasContent("snippets") {
			return &f.Storages[i]
		}
	}
	return nil
}

// RenderNode и CardNode — узлы прямого доступа к видеокарте. Первый нужен
// для вычислений и декодирования видео, второй — для вывода изображения.
func (f *Facts) RenderNode() string { return findNode(f.DRINodes, "renderD") }
func (f *Facts) CardNode() string   { return findNode(f.DRINodes, "/card") }

func findNode(nodes []string, want string) string {
	for _, n := range nodes {
		if strings.Contains(n, want) {
			return n
		}
	}
	return ""
}
