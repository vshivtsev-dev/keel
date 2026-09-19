package ui

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vshivtsev-dev/keel/internal/cli"
)

// Run открывает экран keel.
//
// Без терминала экран не рисуется, и притворяться бессмысленно: в трубе и
// в systemd-юните нужен обычный текст. Поэтому здесь честный отказ с
// подсказкой, чем его заменить.
func Run(ctx context.Context, app *cli.App) error {
	if !isTerminal(os.Stdout) {
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

// isTerminal — проверка без зависимостей: символьное устройство и есть
// терминал. Тащить ради одной строки библиотеку, которая поднимет
// требование к версии Go, в инструменте восстановления не стоит.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
