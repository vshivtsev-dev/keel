package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreKeepsSecretsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	s := NewStore(dir)

	if s.Has("102") {
		t.Fatal("пустой каталог отдал секрет")
	}
	if err := s.Put("102", "тайна"); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(filepath.Join(dir, "102.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("права на файл секрета %v, ожидалось 0600", st.Mode().Perm())
	}
	if d, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if d.Mode().Perm() != 0o700 {
		t.Errorf("права на каталог секретов %v, ожидалось 0700", d.Mode().Perm())
	}

	got, err := s.Get("102")
	if err != nil {
		t.Fatal(err)
	}
	if got != "тайна" {
		t.Errorf("секрет прочитан как %q", got)
	}
}

func TestEnsureAsksOnlyOnce(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "secrets"))

	asked := 0
	ask := func() (string, error) { asked++; return "первый", nil }

	for i := 0; i < 3; i++ {
		v, err := s.Ensure("102", ask)
		if err != nil {
			t.Fatal(err)
		}
		if v != "первый" {
			t.Errorf("получено %q, ожидалось «первый»", v)
		}
	}
	if asked != 1 {
		t.Errorf("спросили %d раз, а должны были один", asked)
	}
}

func TestEnsureGeneratesOnEmptyAnswer(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "secrets"))
	v, err := s.Ensure("102", func() (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 16 {
		t.Errorf("сгенерирован слишком короткий пароль: %q", v)
	}
	// Пароль иногда набирают с бумажки, глядя в консоль.
	for _, bad := range []string{"0", "O", "1", "l", "I"} {
		if strings.Contains(v, bad) {
			t.Errorf("в пароле %q есть неразличимый символ %q", v, bad)
		}
	}
}

func TestGenerateGivesDifferentPasswords(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := Generate(20)
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 20 {
			t.Fatalf("длина %d вместо 20", len(v))
		}
		if seen[v] {
			t.Fatalf("пароль повторился: %q", v)
		}
		seen[v] = true
	}
}

// Секрет попадает в команду, которую keel показывает до выполнения.
// Показать его нельзя ни разу — ни целиком, ни куском.
func TestMaskerHidesSecrets(t *testing.T) {
	m := NewMasker()
	m.Add("супертайна", "токен-cloudflare")

	got := m.Apply("pct exec 102 -- passwd --stdin супертайна && echo токен-cloudflare")
	if strings.Contains(got, "супертайна") || strings.Contains(got, "токен-cloudflare") {
		t.Fatalf("секрет остался на экране: %s", got)
	}
	if !strings.Contains(got, "pct exec 102") {
		t.Errorf("замаскировано лишнее: %s", got)
	}
}

// Короткий секрет, будучи куском длинного, разрезал бы длинный пополам —
// и вторая половина осталась бы на экране.
func TestMaskerHandlesOverlappingSecrets(t *testing.T) {
	m := NewMasker()
	m.Add("тайна", "тайна-подлиннее")

	got := m.Apply("значение: тайна-подлиннее")
	if strings.Contains(got, "подлиннее") {
		t.Fatalf("длинный секрет утёк по частям: %s", got)
	}
}

// Двухсимвольное значение встречается в любой команде: замаскировав его,
// keel превратил бы экран в звёздочки и спрятал бы настоящую ошибку.
func TestMaskerIgnoresTooShortValues(t *testing.T) {
	m := NewMasker()
	m.Add("ab")
	if got := m.Apply("qm create 100 --name ab"); !strings.Contains(got, "ab") {
		t.Errorf("слишком короткое значение всё же замаскировано: %s", got)
	}
}
