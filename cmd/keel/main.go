// Команда keel — сборка и восстановление хоста Proxmox VE по описанию.
//
// Это новая, переписанная на Go реализация. Пока перенесены не все области:
// то, чего здесь ещё нет, продолжает работать в bash-версии рядом.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/vshivtsev-dev/keel/internal/cli"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/ui"
)

// version подставляется при сборке: -ldflags "-X main.version=0.3.0".
var version = "dev"

const usage = `keel — сборка и восстановление хоста Proxmox VE.

Использование:
  keel [флаги] команда [аргумент]

Команды:
  (без команды) открыть экран keel
  doctor      отчёт о состоянии хоста — только чтение
  plan        собрать план изменений и сохранить его файлом
  apply [ФАЙЛ] применить план (без аргумента — последний собранный)
  verify      сверить хост с манифестом
  gpu revert  откатить проброс видеокарты
  gpu status  что записано о пробросе
  guests      гости из манифеста и их состояние
  password ID пароль, сохранённый для гостя
  token [ИМЯ] сохранённый токен (по умолчанию cloudflared)
  backup      снять копию конфигурации хоста прямо сейчас
  external    внешние инструменты (community-scripts)
  logs        показать предыдущий лог
  validate    проверить манифест на ошибки
  init        разложить каталог keel и создать манифест
  update      обновить сам keel, сверив контрольную сумму
  version     версия
  help        эта справка

Флаги:
  --yes            не спрашивать перед каждым изменением
  --dry-run        ничего не выполнять, только показывать
  --only ID        один провайдер, например: --only host/storage
  --guest ID       только эти гости, через запятую: --guest 100,101
  --manifest ПУТЬ  другой файл манифеста
  --stale          применить план, разошедшийся с состоянием хоста
  --json           машиночитаемый вывод
  --commands       только команды, по одной на строку
  --no-color       без цвета

Порядок, который экономит нервы:
  keel doctor  →  keel plan  →  keel apply
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	var (
		opts     cli.Options
		cmd      string
		planArg  string
		showHelp bool
	)
	opts.Mode = cli.ModeStep

	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; a {
		case "--yes", "-y":
			opts.Mode = cli.ModeYes
		case "--dry-run":
			opts.Mode = cli.ModeDry
		case "--stale":
			opts.Stale = true
		case "--json":
			opts.JSON = true
		case "--commands":
			opts.Commands = true
		case "--no-color":
			os.Setenv("KEEL_NO_COLOR", "1")
		case "--guest":
			v, err := next(argv, &i, "--guest")
			if err != nil {
				return err
			}
			if opts.Guests == nil {
				opts.Guests = map[int]bool{}
			}
			for _, part := range strings.Split(v, ",") {
				n, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil {
					return fmt.Errorf("--guest: %q — это не номер гостя", part)
				}
				opts.Guests[n] = true
			}
		case "--only":
			var err error
			if opts.Only, err = next(argv, &i, "--only"); err != nil {
				return err
			}
		case "--manifest":
			var err error
			if opts.Manifest, err = next(argv, &i, "--manifest"); err != nil {
				return err
			}
		case "-h", "--help":
			showHelp = true
		default:
			if len(a) > 1 && a[0] == '-' {
				return fmt.Errorf("неизвестный флаг: %s (см. keel help)", a)
			}
			switch {
			case cmd == "":
				cmd = a
			case planArg == "":
				planArg = a
			}
		}
	}

	if showHelp || cmd == "help" {
		fmt.Print(usage)
		return nil
	}
	// Без команды открывается экран: это и есть главный способ работы с
	// keel, а команды — для скриптов и для консоли без терминала.
	if cmd == "" {
		cmd = "menu"
	}

	// Ctrl-C должен останавливать долгое чтение и применение, а не висеть.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := paths.New()

	switch cmd {
	case "version":
		fmt.Printf("keel %s\n", version)
		return nil
	case "doctor":
		return cli.Doctor(ctx, os.Stdout, p, exec.System{}, opts.JSON)
	case "init":
		return cli.Init(os.Stdout, p)
	case "update":
		return cli.Update(ctx, os.Stdout, version)
	case "validate":
		path := opts.Manifest
		if path == "" {
			path = p.Manifest()
		}
		if err := cli.Validate(os.Stdout, path); err != nil {
			return err
		}
		fmt.Println("✓ Манифест в порядке.")
		return nil
	}

	app := cli.NewApp(os.Stdout, p, opts, version)
	defer app.Close()

	switch cmd {
	case "menu":
		return ui.Run(ctx, app)
	case "plan":
		return cli.Plan(ctx, app)
	case "apply":
		return cli.Apply(ctx, app, planArg)
	case "verify":
		return cli.Verify(ctx, app)
	case "gpu":
		switch planArg {
		case "revert":
			return cli.GPURevert(ctx, app)
		case "status", "":
			return cli.GPUStatus(app)
		default:
			return fmt.Errorf("keel gpu revert — откатить проброс; keel gpu status — что записано")
		}
	case "guests":
		return cli.Guests(ctx, app)
	case "password":
		return cli.Password(app, planArg)
	case "token":
		return cli.Token(app, planArg)
	case "logs":
		return cli.Logs(app)
	case "external":
		return cli.External(ctx, app, planArg)
	case "backup":
		// Снять копию конфигурации прямо сейчас — это тот же план, только
		// суженный до одного провайдера.
		app.Opts.Only = "host/config-backup"
		return cli.Apply(ctx, app, "")
	default:
		return fmt.Errorf("неизвестная команда: %s (см. keel help)", cmd)
	}
}

func next(argv []string, i *int, flag string) (string, error) {
	*i++
	if *i >= len(argv) {
		return "", errors.New("у " + flag + " не указано значение")
	}
	return argv[*i], nil
}
