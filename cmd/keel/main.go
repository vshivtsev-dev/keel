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
	"syscall"

	"github.com/vshivtsev-dev/keel/internal/cli"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
)

// version подставляется при сборке: -ldflags "-X main.version=0.3.0".
var version = "dev"

const usage = `keel — сборка и восстановление хоста Proxmox VE.

Использование:
  keel [флаги] команда

Команды:
  doctor      отчёт о состоянии хоста — только чтение
  validate    проверить манифест на ошибки
  version     версия
  help        эта справка

Флаги:
  --manifest ПУТЬ  другой файл манифеста
  --json           машиночитаемый вывод (doctor)
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
		cmd          string
		manifestPath string
		asJSON       bool
	)

	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; a {
		case "--json":
			asJSON = true
		case "--no-color":
			os.Setenv("KEEL_NO_COLOR", "1")
		case "--manifest":
			i++
			if i >= len(argv) {
				return errors.New("у --manifest не указан путь")
			}
			manifestPath = argv[i]
		case "-h", "--help":
			fmt.Print(usage)
			return nil
		default:
			if len(a) > 1 && a[0] == '-' {
				return fmt.Errorf("неизвестный флаг: %s (см. keel help)", a)
			}
			if cmd == "" {
				cmd = a
			}
		}
	}

	// Ctrl-C должен останавливать долгое чтение фактов, а не висеть.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := paths.New()
	if manifestPath == "" {
		manifestPath = p.Manifest()
	}

	switch cmd {
	case "", "help":
		fmt.Print(usage)
		return nil
	case "version":
		fmt.Printf("keel %s\n", version)
		return nil
	case "doctor":
		return cli.Doctor(ctx, os.Stdout, p, exec.System{}, asJSON)
	case "validate":
		if err := cli.Validate(os.Stdout, manifestPath); err != nil {
			return err
		}
		fmt.Println("✓ Манифест в порядке.")
		return nil
	case "plan", "apply", "verify", "menu", "guests", "password", "token", "gpu", "external", "logs", "backup":
		return fmt.Errorf("команда %q ещё не перенесена на Go — пока пользуйся bash-версией: %s/bin/keel %s",
			cmd, repoHint(), cmd)
	default:
		return fmt.Errorf("неизвестная команда: %s (см. keel help)", cmd)
	}
}

func repoHint() string {
	if v := os.Getenv("KEEL_HOME"); v != "" {
		return v + "/app"
	}
	return "/root/keel/app"
}
