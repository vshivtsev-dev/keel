package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// dist поднимает раздачу, какой её отдаёт nginx из образа.
func dist(t *testing.T, binary []byte, version string, corrupt bool) string {
	t.Helper()
	name := "keel-linux-" + runtime.GOARCH
	sum := sha256.Sum256(binary)
	if corrupt {
		// Сумма от другого содержимого — ровно то, что увидел бы человек
		// при подменённом или недокачанном файле.
		sum = sha256.Sum256([]byte("совсем другое"))
	}
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/VERSION":
			fmt.Fprintln(w, version)
		case "/checksums.txt":
			fmt.Fprint(w, sums)
		case "/" + name:
			w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeSelf подменяет «сам бинарник» файлом во временном каталоге.
func fakeSelf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keel")
	if err := os.WriteFile(path, []byte("старый keel"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUpdateRefusesOnChecksumMismatch(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("обновление требует прав root")
	}
	self := fakeSelf(t)
	t.Setenv("KEEL_DIST_URL", dist(t, []byte("новый keel"), "0.4.0", true))

	out := &bytes.Buffer{}
	err := updateInto(context.Background(), out, "0.3.0", self)
	if err == nil {
		t.Fatal("бинарник с неверной суммой принят")
	}
	if !strings.Contains(err.Error(), "не сошлась") {
		t.Errorf("ошибка не называет причину: %v", err)
	}
	// Старый бинарник должен остаться нетронутым.
	got, _ := os.ReadFile(self)
	if string(got) != "старый keel" {
		t.Errorf("старый бинарник испорчен: %q", got)
	}
}

func TestUpdateReplacesBinary(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("обновление требует прав root")
	}
	self := fakeSelf(t)
	t.Setenv("KEEL_DIST_URL", dist(t, []byte("новый keel"), "0.4.0", false))

	if err := updateInto(context.Background(), &bytes.Buffer{}, "0.3.0", self); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "новый keel" {
		t.Errorf("бинарник не заменён: %q", got)
	}
	st, _ := os.Stat(self)
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("новый бинарник не исполняемый: %v", st.Mode().Perm())
	}
}

// Скачивать заново то же самое — пустая трата времени и трафика.
func TestUpdateSkipsWhenAlreadyLatest(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("обновление требует прав root")
	}
	self := fakeSelf(t)
	t.Setenv("KEEL_DIST_URL", dist(t, []byte("новый keel"), "0.3.0", false))

	out := &bytes.Buffer{}
	if err := updateInto(context.Background(), out, "0.3.0", self); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "нечего делать") {
		t.Errorf("не сказано, что обновляться незачем:\n%s", out.String())
	}
	got, _ := os.ReadFile(self)
	if string(got) != "старый keel" {
		t.Error("бинарник заменён, хотя версия та же")
	}
}

func TestSumFor(t *testing.T) {
	sums := "aaa  keel-linux-arm64\nbbb  keel-linux-amd64\n"
	if got := sumFor(sums, "keel-linux-amd64"); got != "bbb" {
		t.Errorf("получено %q", got)
	}
	if got := sumFor(sums, "нет-такого"); got != "" {
		t.Errorf("найдена сумма несуществующего файла: %q", got)
	}
}
