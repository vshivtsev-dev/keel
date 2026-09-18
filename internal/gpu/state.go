// Package gpu хранит запись о пробросе видеокарты и умеет его откатить.
//
// Запись существует ровно для одного случая: хост не загрузился, монитор
// погас, и человек чинит машину вслепую или с live-USB. Поэтому она лежит
// обычным JSON рядом с манифестом, читается глазами и не зависит ни от
// чего, кроме файловой системы.
package gpu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State — что именно keel изменил, отдавая видеокарту.
type State struct {
	Created time.Time `json:"created"`
	Device  string    `json:"device"`
	IDs     string    `json:"ids"`
	VM      string    `json:"vm"`
	// Stamp — метка каталога с резервными копиями. По ней и находятся
	// файлы, которые надо вернуть.
	Stamp      string `json:"stamp"`
	Bootloader string `json:"bootloader"`
	// Changed — файлы, существовавшие до нас: их возвращают из копий.
	Changed []string `json:"changed"`
	// CreatedFiles — файлы, которых до нас не было: их удаляют.
	CreatedFiles []string `json:"created_files"`
}

func Path(home string) string { return filepath.Join(home, "gpu-passthrough.state") }

func Save(home string, s *State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(Path(home), append(raw, '\n'), 0o600)
}

func Load(home string) (*State, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return nil, fmt.Errorf("нет записи о пробросе (%s) — откатывать нечего.\n"+
			"Если правки делались руками, смотри копии в backups/ и docs/30-desktop.md", Path(home))
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("запись о пробросе %s не читается: %w", Path(home), err)
	}
	return &s, nil
}

func Remove(home string) error {
	err := os.Remove(Path(home))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RevertInstructions печатаются ДО перезагрузки: после неё читать будет негде.
func RevertInstructions(home, backups, modprobeFile string) string {
	return fmt.Sprintf(`Обычный откат, с работающего хоста:

    keel gpu revert
    reboot

Если хост не загрузился и монитора нет — с live-USB:

    1. загрузиться с любого live-образа Linux;
    2. смонтировать корневой раздел хоста, например в /mnt;
    3. rm /mnt%s
    4. перезагрузиться.

Файл с записью обо всех изменениях: %s
Резервные копии изменённых файлов:  %s/
`, modprobeFile, Path(home), backups)
}
