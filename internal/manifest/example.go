package manifest

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// Пример манифеста вкомпилирован в бинарник по той же причине, что и
// профили: keel ставится одним файлом на хост, который только что
// переустановили, и взять пример ему больше негде.
//
//go:embed host.example.json
var example []byte

// Example — пример манифеста с комментариями.
func Example() []byte { return example }

// Init раскладывает каталог keel и кладёт манифест, если его ещё нет.
//
// Существующий манифест не трогается никогда: это единственный файл,
// который человек правит руками, и перезаписать его значит стереть
// описание хоста — ровно то, ради чего keel и существует.
func Init(home string) (created bool, err error) {
	for _, dir := range []string{home, filepath.Join(home, "secrets"),
		filepath.Join(home, "logs"), filepath.Join(home, "backups"),
		filepath.Join(home, "plans")} {
		mode := os.FileMode(0o755)
		// В secrets/ лежат пароли и токены — туда посторонним не надо.
		if filepath.Base(dir) == "secrets" {
			mode = 0o700
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return false, fmt.Errorf("каталог %s: %w", dir, err)
		}
	}

	path := filepath.Join(home, "host.json")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.WriteFile(path, example, 0o644); err != nil {
		return false, fmt.Errorf("манифест %s: %w", path, err)
	}
	return true, nil
}
