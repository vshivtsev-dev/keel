package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vshivtsev-dev/keel/internal/engine"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/image"
	"github.com/vshivtsev-dev/keel/internal/journal"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/provider"
	"github.com/vshivtsev-dev/keel/internal/provider/guests"
	"github.com/vshivtsev-dev/keel/internal/provider/host"
	"github.com/vshivtsev-dev/keel/internal/secret"
)

// Mode — как вести себя с изменениями.
type Mode string

const (
	// ModeStep спрашивает перед каждым изменением. По умолчанию.
	ModeStep Mode = "step"
	// ModeYes не спрашивает: план уже посмотрен и подтверждён целиком.
	ModeYes Mode = "yes"
	// ModeDry ничего не выполняет, только показывает.
	ModeDry Mode = "dry"
)

// Options — то, что задаётся флагами.
type Options struct {
	Manifest string
	Only     string
	// Guests сужает работу до перечисленных гостей: этим пользуется
	// флаг --guest и экраны выбора.
	Guests map[int]bool
	Mode   Mode
	JSON   bool
	// Commands печатает только команды, по одной на строку.
	Commands bool
	// Stale разрешает применить план, разошедшийся с состоянием хоста.
	Stale bool
}

// App — собранное окружение одного запуска keel.
type App struct {
	Out      io.Writer
	Paths    paths.Paths
	Opts     Options
	Registry *provider.Registry
	Capturer exec.Capturer
	Secrets  *secret.Store
	Masker   *secret.Masker
	Log      *journal.Log
	Version  string
}

// Registry — порядок применения провайдеров. Он важен: хранилища заводятся
// раньше гостей, которые на них лягут. Порядок задаётся здесь явно, а не
// именами файлов, как было в bash.
func Registry(p paths.Paths, only map[int]bool) *provider.Registry {
	return provider.NewRegistry(
		host.Repos{},
		host.Updates{},
		host.Storage{},
		// Гости создаются после хранилищ: им нужно, куда лечь, и нужны
		// сниппеты для cloud-init, которые включает провайдер хранилищ.
		guests.Guests{
			Images:   image.NewResolver(),
			Only:     only,
			CacheDir: filepath.Join(p.Home(), "images"),
		},
		host.Backup{},
		// Проброс видеокарты идёт после гостей: отдавать карту
		// несуществующей ВМ нечему.
		host.GPU{Paths: p, Capturer: exec.System{}},
		host.ConfigBackup{},
	)
}

func NewApp(out io.Writer, p paths.Paths, opts Options, version string) *App {
	masker := secret.NewMasker()
	return &App{
		Out:      out,
		Paths:    p,
		Opts:     opts,
		Registry: Registry(p, opts.Guests),
		Capturer: exec.System{},
		Secrets:  secret.NewStore(p.Secrets()),
		Masker:   masker,
		Log:      journal.Open(p.Logs(), masker.Apply),
		Version:  version,
	}
}

func (a *App) Close() error { return a.Log.Close() }

// Manifest читает манифест, учитывая флаг --manifest.
func (a *App) LoadManifest() (*manifest.Manifest, error) {
	path := a.Opts.Manifest
	if path == "" {
		path = a.Paths.Manifest()
	}
	return manifest.Load(path)
}

func (a *App) Facts(ctx context.Context) *facts.Facts {
	return facts.Collect(ctx, a.Paths, a.Capturer)
}

// FactsFor добирает то, что зависит от манифеста: каталог с копиями
// конфигурации задаётся им, а не известен заранее, как прочие места
// на хосте.
func (a *App) FactsFor(ctx context.Context, m *manifest.Manifest) *facts.Facts {
	f := a.Facts(ctx)
	if cb := m.Host.ConfigBackup; cb != nil {
		f.ConfigArchivesIn(a.Paths.Sys, cb.Path)
	}
	return f
}

// Providers сужает набор провайдеров флагом --only.
func (a *App) Providers() (*provider.Registry, error) {
	return a.Registry.Only(a.Opts.Only)
}

// Engine собирает движок, уже знающий про KEEL_FS_ROOT.
func (a *App) Engine() (*engine.Engine, error) {
	reg, err := a.Providers()
	if err != nil {
		return nil, err
	}
	return engine.New(reg, a.Paths.Sys, a.Version), nil
}

// NeedRoot отказывается менять систему без прав. Проверка стоит перед
// изменениями, а не перед чтением: doctor и plan должны работать всегда.
func NeedRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("нужны права root — запусти под root или через sudo")
	}
	return nil
}

// NeedPVE отказывается применять что-либо к машине, которая не является
// хостом Proxmox. Обойти можно осознанно.
func NeedPVE(f *facts.Facts, p paths.Paths) error {
	if p.Sandboxed() || f.IsPVE || os.Getenv("KEEL_ALLOW_NON_PVE") == "1" {
		return nil
	}
	return fmt.Errorf("это не хост Proxmox VE; если уверен — KEEL_ALLOW_NON_PVE=1 keel apply")
}
