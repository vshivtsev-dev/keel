package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// sheet печатает ровные колонки в обычный терминал. Цвет включается только
// когда вывод и правда идёт на экран: в трубе и в файле управляющие
// последовательности только мешают, а NO_COLOR — общепринятый способ
// сказать «не надо», и его надо слушать.
//
// «Настоящий экран» определяется term.IsTerminal, а не тем, символьное ли
// это устройство. Разница не теоретическая: /dev/null — тоже символьное
// устройство, и самодельная проверка красила вывод в никуда.
type sheet struct {
	w     io.Writer
	color bool
}

const labelWidth = 22

func newSheet(w io.Writer) *sheet {
	return &sheet{w: w, color: colorEnabled(w)}
}

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("KEEL_NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func (s *sheet) paint(code, text string) string {
	if !s.color {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

func (s *sheet) section(title string) {
	fmt.Fprintf(s.w, "\n%s\n", s.paint("1", title))
}

func (s *sheet) row(label, value string) {
	fmt.Fprintf(s.w, "  %s %s\n", pad(label, labelWidth), value)
}

func (s *sheet) ok(label, value string) {
	fmt.Fprintf(s.w, "  %s %s %s\n", s.paint("32", "✓"), pad(label, labelWidth-2), value)
}

func (s *sheet) warn(label, value string) {
	fmt.Fprintf(s.w, "  %s %s %s\n", s.paint("33", "!"), pad(label, labelWidth-2), value)
}

func (s *sheet) change(label, value string) {
	fmt.Fprintf(s.w, "  %s %s %s\n", s.paint("34", "→"), pad(label, labelWidth-2), value)
}

// skip — правило нуля: в манифесте про эту область ничего не сказано.
// Значок нарочно неприметный: это не проблема, а норма.
func (s *sheet) skip(label, value string) {
	fmt.Fprintf(s.w, "  %s %s %s\n", s.paint("2", "·"), pad(label, labelWidth-2), s.paint("2", value))
}

func (s *sheet) bad(label, value string) {
	fmt.Fprintf(s.w, "  %s %s %s\n", s.paint("31", "✗"), pad(label, labelWidth-2), value)
}

func (s *sheet) indent(text string) {
	fmt.Fprintf(s.w, "      %s\n", text)
}

func (s *sheet) note(text string) {
	fmt.Fprintf(s.w, "  %s\n", s.paint("2", text))
}

// pad считает символы, а не байты: русские подписи многобайтовые, и
// printf("%-20s") разъезжается на них колонками.
func pad(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
