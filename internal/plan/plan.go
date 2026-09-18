package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FormatVersion растёт, когда меняется раскладка файла плана. Старый план
// с чужой версией не исполняется: лучше пересобрать, чем угадывать.
const FormatVersion = 1

// Plan — набор изменений, собранный в один момент времени.
//
// Это артефакт: он сохраняется на диск и применяется именно в том виде, в
// каком его посмотрел человек. Раньше plan и apply считали изменения
// независимо, и между ними состояние хоста могло поменяться.
type Plan struct {
	Version   int       `json:"version"`
	Created   time.Time `json:"created"`
	Host      string    `json:"host"`
	KeelVer   string    `json:"keel_version"`
	Manifest  string    `json:"manifest_path"`
	ManifestS string    `json:"manifest_sha256"`
	// FactsDigest — отпечаток состояния хоста на момент сборки. Если перед
	// применением он не сошёлся, значит хост изменился и план устарел.
	FactsDigest string `json:"facts_digest"`

	Steps []Step `json:"steps"`
}

// Empty сообщает, что менять нечего. Это нормальный исход, а не ошибка.
func (p *Plan) Empty() bool { return len(p.Steps) == 0 }

// Providers перечисляет провайдеров, у которых есть шаги, в порядке появления.
func (p *Plan) Providers() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range p.Steps {
		if !seen[s.Provider] {
			seen[s.Provider] = true
			out = append(out, s.Provider)
		}
	}
	return out
}

// Select оставляет шаги выбранных провайдеров и ресурсов. Пустой выбор
// означает «всё»: так `keel apply` без уточнений применяет весь план.
func (p *Plan) Select(ids map[string]bool) *Plan {
	if len(ids) == 0 {
		return p
	}
	out := *p
	out.Steps = nil
	for _, s := range p.Steps {
		if ids[s.ID] || ids[s.Provider] {
			out.Steps = append(out.Steps, s)
		}
	}
	return &out
}

// Save записывает план в каталог планов и возвращает путь.
func (p *Plan) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("каталог планов %s: %w", dir, err)
	}
	name := p.Created.Format("2006-01-02_150405") + ".json"
	path := filepath.Join(dir, name)
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	// 0600: в плане нет секретов, но есть подробная карта хоста.
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// LoadFile читает сохранённый план.
func LoadFile(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("план %s: %w", path, err)
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("план %s: %w", path, err)
	}
	if p.Version != FormatVersion {
		return nil, fmt.Errorf("план %s собран версией формата %d, а нужна %d — пересобери: keel plan",
			path, p.Version, FormatVersion)
	}
	return &p, nil
}

// Latest возвращает путь к последнему сохранённому плану.
func Latest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("каталог планов %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("в %s нет ни одного плана — собери: keel plan", dir)
	}
	// Имя плана — это метка времени, поэтому лексикографический порядок
	// совпадает с хронологическим.
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

// Stale сообщает, разошёлся ли план с текущим состоянием хоста.
func (p *Plan) Stale(manifestSHA, factsDigest string) (bool, string) {
	switch {
	case p.ManifestS != manifestSHA:
		return true, "манифест изменился после сборки плана"
	case p.FactsDigest != factsDigest:
		return true, "состояние хоста изменилось после сборки плана"
	}
	return false, ""
}

// Digest считает отпечаток произвольного набора строк. Используется и для
// манифеста, и для фактов о хосте.
func Digest(parts ...string) string {
	h := sha256.New()
	for _, s := range parts {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
