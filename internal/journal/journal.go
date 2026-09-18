// Package journal — лог одного запуска keel.
//
// В лог попадает всё: какой шаг собирались выполнить, какую команду для
// этого построили, что она ответила, чем кончилось. Когда что-то пошло не
// так на хосте без монитора, этот файл — единственное, что остаётся.
package journal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Log struct {
	mu   sync.Mutex
	file *os.File
	path string
	mask func(string) string
}

// Open заводит лог в каталоге dir. Не смогли писать туда — пишем во
// временный каталог: отсутствие прав на лог не повод отказываться работать.
func Open(dir string, mask func(string) string) *Log {
	if mask == nil {
		mask = func(s string) string { return s }
	}
	l := &Log{mask: mask}

	name := time.Now().Format("2006-01-02_150405") + ".log"
	for _, d := range []string{dir, filepath.Join(os.TempDir(), "keel-logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			continue
		}
		f, err := os.OpenFile(filepath.Join(d, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			continue
		}
		l.file, l.path = f, filepath.Join(d, name)
		break
	}
	return l
}

func (l *Log) Path() string { return l.path }

func (l *Log) Printf(format string, args ...any) {
	l.write(fmt.Sprintf(format, args...))
}

// Write позволяет отдать лог как io.Writer — так в него утекает вывод
// выполняемых команд, слово в слово.
func (l *Log) Write(p []byte) (int, error) {
	l.writeRaw(l.mask(string(p)))
	return len(p), nil
}

func (l *Log) write(msg string) {
	l.writeRaw(time.Now().Format("2006-01-02 15:04:05") + " " + l.mask(msg) + "\n")
}

func (l *Log) writeRaw(s string) {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.file, s)
}

func (l *Log) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
