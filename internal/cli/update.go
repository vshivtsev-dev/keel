package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DistURL — откуда keel берёт свои же обновления. Подменяется при сборке:
// -ldflags "-X github.com/.../internal/cli.DistURL=https://…".
var DistURL = "https://keel.example.invalid"

// Update заменяет keel на свежий, сверив контрольную сумму.
//
// Сверка нужна ровно потому, что keel перестал быть читаемым скриптом.
// Раньше человек мог открыть скачанное и посмотреть, что там; теперь это
// непрозрачный бинарник, и единственное, что остаётся, — убедиться, что
// приехало именно то, что собиралось.
//
// От человека для этого ничего не требуется: сумма едет вместе с
// бинарником и проверяется молча.
func Update(ctx context.Context, w io.Writer, version string) error {
	if err := NeedRoot(); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не понял, где лежу сам: %w", err)
	}
	// По симлинку идём до настоящего файла: /usr/local/bin/keel — это
	// ссылка, и заменять надо то, на что она указывает.
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return updateInto(ctx, w, version, self)
}

func updateInto(ctx context.Context, w io.Writer, version, self string) error {
	base := strings.TrimRight(distURL(), "/")

	s := newSheet(w)
	s.section("Обновление keel")
	s.row("сейчас", version)
	s.row("откуда", base)

	name := "keel-linux-" + runtime.GOARCH
	client := &http.Client{Timeout: 5 * time.Minute}

	remote, err := fetchText(ctx, client, base+"/VERSION")
	if err == nil {
		remote = strings.TrimSpace(remote)
		s.row("там", remote)
		if remote == version {
			s.ok("нечего делать", "стоит самая свежая версия")
			return nil
		}
	}

	sums, err := fetchText(ctx, client, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("не удалось получить контрольные суммы: %w", err)
	}
	want := sumFor(sums, name)
	if want == "" {
		return fmt.Errorf("в checksums.txt нет строки про %s — раздача собрана неправильно", name)
	}

	body, err := fetchBytes(ctx, client, base+"/"+name)
	if err != nil {
		return fmt.Errorf("не удалось скачать %s: %w", name, err)
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("контрольная сумма не сошлась.\n"+
			"  ожидалась: %s\n  получена:  %s\n"+
			"Не ставлю: скачалось не то, что собиралось", want, hex.EncodeToString(got[:]))
	}
	s.ok("сумма сошлась", want[:16]+"…")

	// Записываем рядом и переименовываем: так на месте старого файла в
	// любой момент лежит либо целый старый, либо целый новый. Записать
	// поверх работающего бинарника нельзя — ядро его держит.
	tmp := self + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		return fmt.Errorf("не удалось записать %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("не удалось заменить %s: %w", self, err)
	}
	s.ok("обновлено", self)
	return nil
}

func distURL() string {
	if v := os.Getenv("KEEL_DIST_URL"); v != "" {
		return v
	}
	return DistURL
}

// sumFor ищет строку вида «<сумма>  <имя файла>».
func sumFor(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0]
		}
	}
	return ""
}

func fetchText(ctx context.Context, c *http.Client, url string) (string, error) {
	raw, err := fetchBytes(ctx, c, url)
	return string(raw), err
}

func fetchBytes(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s ответил %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
