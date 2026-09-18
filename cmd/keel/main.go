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
)

// version подставляется при сборке: -ldflags "-X main.version=0.3.0".
var version = "dev"

const usage = `keel — сборка и восстановление хоста Proxmox VE.

Использование:
  keel [флаги] команда [аргумент]

Команды:
  doctor      отчёт о состоянии хоста — только чтение
  plan        собрать план изменений и сохранить его файлом
  apply [ФАЙЛ] применить план (без аргумента — последний собранный)
  verify      сверить хост с манифестом
  validate    проверить манифест на ошибки
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

	if showHelp || cmd == "" || cmd == "help" {
		fmt.Print(usage)
		return nil
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
	case "plan":
		return cli.Plan(ctx, app)
	case "apply":
		return cli.Apply(ctx, app, planArg)
	case "verify":
		return cli.Verify(ctx, app)
	case "menu", "guests", "password", "token", "gpu", "external", "logs", "backup":
		return fmt.Errorf("команда %q ещё не перенесена на Go — пока пользуйся bash-версией: %s/bin/keel %s",
			cmd, bashHome(), cmd)
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

func bashHome() string {
	if v := os.Getenv("KEEL_HOME"); v != "" {
		return v + "/app"
	}
	return "/root/keel/app"
}
