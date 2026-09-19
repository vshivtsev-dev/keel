package ui

import (
	"strings"
	"sync"
)

// teaWriter собирает вывод выполняемых команд.
//
// Показывать его надо обязательно: dist-upgrade и скачивание образа на
// полгигабайта длятся минутами, и молчащий экран в это время выглядит как
// зависание — человек жмёт Ctrl-C ровно посередине правки конфига.
type teaWriter struct {
	mu    sync.Mutex
	buf   string
	lines []string
}

func (w *teaWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.lines = append(w.lines, strings.TrimRight(w.buf[:i], "\r"))
		w.buf = w.buf[i+1:]
	}
	// Кольцевой буфер: полный вывод всё равно уходит в лог, а держать в
	// памяти всё, что напечатал apt, незачем.
	if len(w.lines) > 500 {
		w.lines = w.lines[len(w.lines)-500:]
	}
	return len(p), nil
}

func (w *teaWriter) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.lines))
	copy(out, w.lines)
	return out
}
