package lint_test

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Страж приватности.
//
// Репозиторий открытый. Значит всё, что в него попало, видно всем —
// включая то, что попало случайно: адрес домашней сети, почта, отпечаток
// ключа, токен, забытый в примере.
//
// Наследник tests/lint-no-secrets.sh из bash-версии. Проверяются только
// файлы под контролем git: то, что лежит рядом и не отслеживается, никуда
// не уедет.
//
// Новое совпадение не проходит молча. Его нужно либо убрать, либо
// осознанно внести в allowed ниже — с объяснением, почему это выдумка.

// allowed — выдуманные примеры, которые должны остаться в документации.
// Каждая строка с объяснением: без него список через год превратится в
// свалку, и страж перестанет что-либо стеречь.
var allowed = map[string]string{
	// Диапазон RFC 5737 существует ровно для примеров в документации.
	// Настоящий частный адрес в примере неотличим от следа чьей-то сети,
	// поэтому в документации keel их быть не должно вовсе.
	"192.0.2.5":  "документационный адрес RFC 5737",
	"192.0.2.50": "документационный адрес RFC 5737",
	"192.0.2.1":  "документационный адрес RFC 5737",
	// Домен, который заведомо не существует и не может быть зарегистрирован.
	"keel.example.invalid":  "заглушка адреса раздачи",
	"keel.example.com":      "пример домена в документации",
	"noreply@anthropic.com": "адрес для подписи коммитов",
	// Документация настойчиво просит не оставлять этот адрес — и именно
	// поэтому он в ней написан.
	"mail@example.com": "адрес-заглушка в инструкции по установке PVE",
}

var patterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"частный адрес IPv4", regexp.MustCompile(
		`\b(192\.168\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})\b`)},
	{"почта", regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},
	{"открытый ключ SSH", regexp.MustCompile(`ssh-(rsa|ed25519) AAAA[A-Za-z0-9+/=]*`)},
	{"закрытый ключ", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"токен GitHub", regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`)},
	{"ключ AWS", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"токен Slack", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..")
}

func TestNoPersonalDataInRepository(t *testing.T) {
	root := repoRoot(t)

	out, err := exec.Command("git", "-C", root, "ls-files").Output()
	if err != nil {
		t.Skipf("git недоступен: %v", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("git не назвал ни одного файла — страж смотрит не туда")
	}

	self := filepath.Base(mustRel(t, root, callerFile(t)))
	checked := 0
	for _, rel := range files {
		// Сам страж не сканируем: в нём совпадения по смыслу.
		if filepath.Base(rel) == self {
			continue
		}
		// Читаем с диска, а не из коммита: страж должен ловить то, что
		// вот-вот уедет, а не то, что уехало в прошлый раз.
		raw := readFile(t, filepath.Join(root, rel))
		if raw == nil {
			continue
		}
		checked++

		for _, p := range patterns {
			for _, m := range p.re.FindAllString(string(raw), -1) {
				if _, ok := allowed[m]; ok {
					continue
				}
				t.Errorf("%s: похоже на настоящие данные (%s): %s\n"+
					"Либо убери это из репозитория, либо внеси в allowed с объяснением.",
					rel, p.name, m)
			}
		}
	}
	if checked == 0 {
		t.Fatal("не проверено ни одного файла")
	}
}
