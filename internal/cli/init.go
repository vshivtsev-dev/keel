package cli

import (
	"fmt"
	"io"

	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/paths"
)

// Init раскладывает каталог keel и кладёт пример манифеста.
//
// Всё, с чем работает человек, лежит в одном месте и больше нигде:
// манифест, пароли, копии файлов, логи, планы. Помнить один путь проще,
// чем четыре, а при восстановлении это разница между «скопировал каталог»
// и «вспоминал, что где лежало».
func Init(w io.Writer, p paths.Paths) error {
	created, err := manifest.Init(p.Home())
	if err != nil {
		return err
	}

	s := newSheet(w)
	s.section("Каталог keel")
	s.row("манифест", p.Manifest())
	s.row("секреты", p.Secrets())
	s.row("копии файлов", p.Backups())
	s.row("планы", p.Plans())
	s.row("логи", p.Logs())

	fmt.Fprintln(w)
	if created {
		s.ok("манифест создан", "поправь его под себя, а потом: keel plan")
	} else {
		s.ok("манифест уже был", "оставил как есть")
	}
	return nil
}
