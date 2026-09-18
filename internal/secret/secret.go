// Package secret хранит пароли и токены и следит, чтобы они не попали на
// экран, в лог и в файл плана.
//
// Секреты не лежат в манифесте никогда: манифест человек правит руками и
// кладёт в git. Они живут отдельными файлами в secrets/ с правами 0600,
// а всё остальное ссылается на них по имени.
package secret

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store — каталог secrets/.
type Store struct {
	dir string
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

// Ref — имя файла в secrets/ без пути: «102» для пароля гостя, «cloudflared»
// для токена. Именно ref попадает в план вместо значения.
func (s *Store) path(ref string) string { return filepath.Join(s.dir, ref+".txt") }

func (s *Store) Has(ref string) bool {
	_, err := os.Stat(s.path(ref))
	return err == nil
}

func (s *Store) Get(ref string) (string, error) {
	raw, err := os.ReadFile(s.path(ref))
	if err != nil {
		return "", fmt.Errorf("секрет %q не сохранён (%s)", ref, s.path(ref))
	}
	return strings.TrimRight(string(raw), "\n"), nil
}

// Put кладёт секрет. Каталог и файл создаются с правами только для
// владельца: на хосте Proxmox это root, и больше никому они не нужны.
func (s *Store) Put(ref, value string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("каталог секретов %s: %w", s.dir, err)
	}
	if err := os.WriteFile(s.path(ref), []byte(value+"\n"), 0o600); err != nil {
		return fmt.Errorf("секрет %q: %w", ref, err)
	}
	return nil
}

// Ensure возвращает уже сохранённый секрет или кладёт новый, полученный от
// ask. Пустой ответ означает «придумай сам».
func (s *Store) Ensure(ref string, ask func() (string, error)) (string, error) {
	if s.Has(ref) {
		return s.Get(ref)
	}
	value, err := ask()
	if err != nil {
		return "", err
	}
	if value == "" {
		if value, err = Generate(20); err != nil {
			return "", err
		}
	}
	if err := s.Put(ref, value); err != nil {
		return "", err
	}
	return value, nil
}

// Generate придумывает пароль. Алфавит без похожих друг на друга символов:
// пароль от рабочего стола иногда приходится набирать с бумажки, глядя в
// консоль, и различить там 0 и O, 1 и l — отдельное мучение.
const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func Generate(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("длина пароля должна быть больше нуля")
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("не удалось придумать пароль: %w", err)
		}
		out[i] = alphabet[k.Int64()]
	}
	return string(out), nil
}

// Masker заменяет секреты звёздочками во всём, что видит человек.
//
// Замена идёт по подстроке, а не регулярным выражением: в пароле могут
// оказаться любые символы, и ошибка в экранировании здесь означает
// утёкший секрет.
type Masker struct {
	mu      sync.RWMutex
	secrets []string
}

func NewMasker() *Masker { return &Masker{} }

func (m *Masker) Add(values ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range values {
		// Слишком короткое значение замаскировало бы пол-экрана: строка
		// из двух символов встречается в любой команде.
		if len(v) < 4 {
			continue
		}
		m.secrets = append(m.secrets, v)
	}
	// Длинные раньше коротких: иначе замена короткого разрежет длинный
	// пополам и вторая половина останется на экране.
	sort.Slice(m.secrets, func(i, j int) bool { return len(m.secrets[i]) > len(m.secrets[j]) })
}

func (m *Masker) Apply(text string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.secrets {
		text = strings.ReplaceAll(text, s, "********")
	}
	return text
}

// ApplyAll маскирует срез строк, не меняя исходный.
func (m *Masker) ApplyAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = m.Apply(s)
	}
	return out
}

// --- Место секрета в плане ----------------------------------------------------
//
// Пароль рабочего стола попадает в сценарий настройки контейнера, токен
// туннеля — в команду установки. А план сохраняется файлом на диск, и
// секретов в нём быть не может.
//
// Поэтому в план кладётся не значение, а метка вида @@keel-secret:102@@.
// Подставляет её тот, кто выполняет шаг, — прямо перед выполнением,
// и только в памяти. В файле плана, в логе и на экране остаётся метка
// или звёздочки.

const (
	markPrefix = "@@keel-secret:"
	markSuffix = "@@"
)

// Mark — метка секрета для подстановки в шаг плана.
func Mark(ref string) string { return markPrefix + ref + markSuffix }

// Resolve подставляет значения секретов вместо меток.
//
// Неизвестная метка — ошибка, а не пустая строка: молча подставленная
// пустота означала бы контейнер с пустым паролем.
func (s *Store) Resolve(text string) (string, error) {
	for {
		start := strings.Index(text, markPrefix)
		if start < 0 {
			return text, nil
		}
		rest := text[start+len(markPrefix):]
		end := strings.Index(rest, markSuffix)
		if end < 0 {
			return "", fmt.Errorf("неполная метка секрета в %q", text[start:])
		}
		ref := rest[:end]
		value, err := s.Get(ref)
		if err != nil {
			return "", err
		}
		text = text[:start] + value + rest[end+len(markSuffix):]
	}
}

// ResolveAll подставляет секреты в срез, не меняя исходный.
func (s *Store) ResolveAll(in []string) ([]string, error) {
	out := make([]string, len(in))
	for i, v := range in {
		got, err := s.Resolve(v)
		if err != nil {
			return nil, err
		}
		out[i] = got
	}
	return out, nil
}

// Refs перечисляет метки, встреченные в тексте.
func Refs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for {
		start := strings.Index(text, markPrefix)
		if start < 0 {
			return out
		}
		rest := text[start+len(markPrefix):]
		end := strings.Index(rest, markSuffix)
		if end < 0 {
			return out
		}
		if ref := rest[:end]; !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
		text = rest[end+len(markSuffix):]
	}
}
