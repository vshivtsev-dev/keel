// Package profile — рецепты гостей: откуда берётся образ, какие значения
// по умолчанию, что доустановить внутри.
//
// Профиль отвечает на вопрос «как», манифест — на вопрос «что». Благодаря
// этому в манифесте лежит «профиль haos, 4 ГБ памяти», а не двадцать строк
// аргументов qm, которые человеку негде взять.
//
// Профили вкомпилированы в бинарник: keel должен работать на хосте,
// который только что переустановили, и искать там свои файлы ему негде.
package profile

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/manifest"
)

//go:embed *.json
var builtin embed.FS

type Profile struct {
	Name  string `json:"-"`
	Kind  string `json:"kind"`
	Title string `json:"title"`

	Image    *Image    `json:"image"`
	Template *Template `json:"template"`

	NeedsPassword bool   `json:"needs_password"`
	NeedsToken    bool   `json:"needs_token"`
	TokenFile     string `json:"token_file"`
	TokenHint     string `json:"token_hint"`
	// TokenCommand — что сделать с токеном внутри гостя. Что именно,
	// знает профиль, а не keel: здесь только подстановка вместо KEEL_TOKEN.
	TokenCommand string `json:"token_command"`
	ReadyHint    string `json:"ready_hint"`

	Defaults Defaults `json:"defaults"`
	VM       VM       `json:"vm"`
	LXC      LXC      `json:"lxc"`

	Packages []string `json:"packages"`
	Runcmd   []string `json:"runcmd"`
}

// Виды гостей. Из вида следует, чем гость создаётся, и второго источника
// правды об этом нет.
const (
	KindVMImage     = "vm-image"
	KindVMCloudInit = "vm-cloudinit"
	KindLXC         = "lxc"
)

type Image struct {
	URL             string `json:"url"`
	URLTemplate     string `json:"url_template"`
	Version         string `json:"version"`
	GitHubRepo      string `json:"github_repo"`
	AssetPattern    string `json:"asset_pattern"`
	FallbackVersion string `json:"fallback_version"`
	Compressed      string `json:"compressed"`
}

type Template struct {
	Pattern string `json:"pattern"`
	Storage string `json:"storage"`
}

type Defaults struct {
	Cores  *int   `json:"cores"`
	Memory *int   `json:"memory"`
	Disk   string `json:"disk"`
}

type VM struct {
	Machine         string `json:"machine"`
	BIOS            string `json:"bios"`
	SCSIHW          string `json:"scsihw"`
	OSType          string `json:"ostype"`
	CPU             string `json:"cpu"`
	Agent           bool   `json:"agent"`
	Serial          bool   `json:"serial"`
	VGA             string `json:"vga"`
	Audio           string `json:"audio"`
	EFIDisk         bool   `json:"efidisk"`
	PreEnrolledKeys bool   `json:"pre_enrolled_keys"`
}

type LXC struct {
	Unprivileged *bool  `json:"unprivileged"`
	OSType       string `json:"ostype"`
	Swap         *int   `json:"swap"`
	Features     string `json:"features"`
	DRI          bool   `json:"dri"`
	VideoGID     *int   `json:"video_gid"`
	RenderGID    *int   `json:"render_gid"`
}

// --- Значения по умолчанию ---------------------------------------------------

func (v VM) MachineOr() string { return or(v.Machine, "q35") }
func (v VM) BIOSOr() string    { return or(v.BIOS, "seabios") }
func (v VM) SCSIHWOr() string  { return or(v.SCSIHW, "virtio-scsi-single") }
func (v VM) OSTypeOr() string  { return or(v.OSType, "l26") }
func (v VM) CPUOr() string     { return or(v.CPU, "host") }

func (l LXC) OSTypeOr() string { return or(l.OSType, "ubuntu") }
func (l LXC) SwapOr() int      { return orInt(l.Swap, 512) }
func (l LXC) UnprivilegedOr() bool {
	return l.Unprivileged == nil || *l.Unprivileged
}
func (l LXC) VideoGIDOr() int  { return orInt(l.VideoGID, 44) }
func (l LXC) RenderGIDOr() int { return orInt(l.RenderGID, 993) }

func (t *Template) StorageOr() string {
	if t == nil {
		return "local"
	}
	return or(t.Storage, "local")
}

func or(v, def string) string {
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

// --- Загрузка ----------------------------------------------------------------

// Load читает встроенный профиль по имени.
func Load(name string) (*Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("не указано имя профиля")
	}
	raw, err := builtin.ReadFile(name + ".json")
	if err != nil {
		return nil, fmt.Errorf("нет профиля %q; есть: %s", name, strings.Join(Names(), ", "))
	}
	// Профили — такой же JSON с комментариями, как манифест: их читают и
	// правят люди, и комментарий в них полезнее, чем отдельный документ.
	std, err := manifest.Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("профиль %s: %w", name, err)
	}
	var p Profile
	if err := json.Unmarshal(std, &p); err != nil {
		return nil, fmt.Errorf("профиль %s: %w", name, err)
	}
	p.Name = name
	if p.Kind == "" {
		return nil, fmt.Errorf("профиль %s: не указан kind", name)
	}
	return &p, nil
}

// Names — все встроенные профили, по алфавиту.
func Names() []string {
	var out []string
	_ = fs.WalkDir(builtin, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		out = append(out, strings.TrimSuffix(path.Base(p), ".json"))
		return nil
	})
	sort.Strings(out)
	return out
}

// Resolve выбирает профиль для записи манифеста.
//
// «desktop» — это роль, а не реализация. Чем её закрыть, решает поле
// graphics: dri — контейнер с видеокартой хоста, virgl — ВМ с 3D через
// virtio-gl, passthrough — ВМ с полностью проброшенной картой.
func Resolve(profileName, graphics string) (string, error) {
	if profileName != "desktop" {
		return profileName, nil
	}
	if graphics == "" {
		graphics = "dri"
	}
	switch graphics {
	case "dri":
		return "desktop-lxc", nil
	case "virgl":
		return "desktop-vm", nil
	case "passthrough":
		return "desktop-vm-gpu", nil
	}
	return "", fmt.Errorf("graphics = %q — допустимо: dri, virgl, passthrough", graphics)
}

// IsVM — создаётся ли гость как виртуальная машина.
func (p *Profile) IsVM() bool { return strings.HasPrefix(p.Kind, "vm-") }
