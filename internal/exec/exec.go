// Package exec — единственный способ keel прикоснуться к системе.
//
// Разделение здесь не косметическое, а несущее:
//
//	Capturer — чтение. Спросить у хоста, как он живёт. Безопасно, ходит
//	           куда угодно, используется при сборе фактов.
//	Runner   — запись. Выполнить шаг плана. Показывает команду до того,
//	           как она случится, снимает копию правленого файла и пишет
//	           в лог. Наследник run() и run_write() из lib/core.sh.
//
// Провайдеры не имеют доступа ни к тому, ни к другому: они описывают шаги,
// а исполняет их движок. За этим следит guard_test.go в internal/provider.
package exec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Capturer выполняет читающую команду и возвращает её вывод.
type Capturer interface {
	Capture(ctx context.Context, name string, args ...string) (string, error)
	// Has сообщает, есть ли команда на хосте. Половина фактов о Proxmox
	// добывается инструментами, которых на обычном Debian нет.
	Has(name string) bool
}

// System — настоящий хост.
type System struct{}

func (System) Capture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s: %s", Render(append([]string{name}, args...)), msg)
	}
	return out.String(), nil
}

func (System) Has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Fake — записывающая подмена для тестов. Ни одной настоящей команды не
// выполняет: отдаёт заранее положенные ответы и запоминает, о чём спросили.
// Прямой наследник заглушек qm/pct/pvesm/pvesh из tests/run.sh.
type Fake struct {
	// Out — ответы по ключу «имя аргумент аргумент».
	Out map[string]string
	// Err — ошибки по тому же ключу.
	Err map[string]error
	// Missing — команды, которых на этом «хосте» нет.
	Missing map[string]bool
	// Calls — всё, о чём спрашивали, по порядку.
	Calls []string
}

func NewFake() *Fake {
	return &Fake{Out: map[string]string{}, Err: map[string]error{}, Missing: map[string]bool{}}
}

func (f *Fake) Capture(_ context.Context, name string, args ...string) (string, error) {
	key := Render(append([]string{name}, args...))
	f.Calls = append(f.Calls, key)
	if err, ok := f.Err[key]; ok {
		return f.Out[key], err
	}
	if out, ok := f.Out[key]; ok {
		return out, nil
	}
	return "", fmt.Errorf("подставная команда не знает ответа на %q", key)
}

func (f *Fake) Has(name string) bool { return !f.Missing[name] }

// Render печатает команду так, чтобы её можно было скопировать в терминал.
// Наследник cmd_str() из lib/core.sh.
func Render(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if needsQuote(a) {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
			b.WriteByte('\'')
			continue
		}
		b.WriteString(a)
	}
	return b.String()
}

func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%^+=:,./-", r):
		default:
			return true
		}
	}
	return false
}
