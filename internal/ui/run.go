package ui

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/vshivtsev-dev/keel/internal/cli"
)

// Run открывает экран keel.
//
// Без терминала экран не рисуется, и притворяться бессмысленно: в трубе и
// в systemd-юните нужен обычный текст. Поэтому здесь честный отказ с
// подсказкой, чем его заменить.
func Run(ctx context.Context, app *cli.App) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("экран keel требует терминала.\n" +
			"Без него те же действия делают команды: keel plan, keel apply, keel verify")
	}

	p := tea.NewProgram(New(ctx, app),
		tea.WithContext(ctx),
		// Мышь нарочно не включается: по SSH она работает через раз, а
		// перехваченное выделение мешает скопировать команду из вывода —
		// то есть ломает ровно то, ради чего команду и показывают.
		tea.WithAltScreen())

	_, err := p.Run()
	return err
}
