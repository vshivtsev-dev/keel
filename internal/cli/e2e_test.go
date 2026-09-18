package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/paths"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Сквозная проверка: манифест → план → файл → применение. Собрана на
// подставном хосте, поэтому гоняется везде и живую машину не трогает.
func sandbox(t *testing.T, manifestBody string) (*App, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	sys := filepath.Join(root, "sys")

	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "host.json"), manifestBody)
	write(filepath.Join(sys, "etc", "pve", "storage.cfg"), `dir: local
	path /var/lib/vz
	content iso,vztmpl,backup

lvmthin: local-lvm
	thinpool data
	content rootdir,images
`)

	out := &bytes.Buffer{}
	p := paths.NewAt(home, sys)

	c := exec.NewFake()
	for _, missing := range []string{"pveversion", "lspci", "ip", "apt-get", "proxmox-boot-tool"} {
		c.Missing[missing] = true
	}

	a := NewApp(out, p, Options{Mode: ModeDry}, "тест")
	a.Capturer = c
	t.Cleanup(func() { _ = a.Close() })
	return a, out, home
}

func TestPlanSavesFileThatApplyCanRead(t *testing.T) {
	a, out, home := sandbox(t, `{"storages":[{"name":"local","content":["iso","snippets"]}]}`)

	if err := Plan(context.Background(), a); err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	if !strings.Contains(out.String(), "snippets") {
		t.Errorf("план не рассказал, что изменится:\n%s", out.String())
	}

	path, err := plan.Latest(filepath.Join(home, "plans"))
	if err != nil {
		t.Fatalf("план не сохранён: %v", err)
	}
	p, err := plan.LoadFile(path)
	if err != nil {
		t.Fatalf("сохранённый план не читается: %v", err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("в плане %d шагов: %+v", len(p.Steps), p.Steps)
	}
	// Команда сохранена дословно — именно её потом и выполнит apply.
	if got := exec.Render(p.Steps[0].Cmd); got != "pvesm set local --content backup,iso,snippets,vztmpl" {
		t.Errorf("команда в файле плана: %s", got)
	}
}

// Правило нуля видно и в выводе: пустой манифест не должен выглядеть как
// «всё уже настроено».
func TestPlanTellsEmptyManifestFromMatchingHost(t *testing.T) {
	a, out, _ := sandbox(t, `{}`)
	if err := Plan(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "в манифесте ничего не описано") {
		t.Errorf("пустой манифест объяснён неверно:\n%s", out.String())
	}

	a2, out2, _ := sandbox(t, `{"storages":[{"name":"local","content":["iso","backup","vztmpl"]}]}`)
	if err := Plan(context.Background(), a2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.String(), "хост уже такой, как описан") {
		t.Errorf("совпадающий хост объяснён неверно:\n%s", out2.String())
	}
}

func TestPlanJSONIsMachineReadable(t *testing.T) {
	a, out, _ := sandbox(t, `{"storages":[{"name":"local","content":["snippets"]}]}`)
	a.Opts.JSON = true

	if err := Plan(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	var p plan.Plan
	if err := decode(out.Bytes(), &p); err != nil {
		t.Fatalf("вывод --json не разбирается: %v\n%s", err, out.String())
	}
	if len(p.Steps) != 1 || p.FactsDigest == "" {
		t.Errorf("в машиночитаемом плане не хватает данных: %+v", p)
	}
}

func TestVerifyReportsDriftAndUnmanaged(t *testing.T) {
	a, out, _ := sandbox(t, `{"storages":[{"name":"local","content":["iso","snippets"]}]}`)
	if err := Verify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "нет типов content: snippets") {
		t.Errorf("расхождение не показано:\n%s", text)
	}
	if !strings.Contains(text, "local-lvm") || !strings.Contains(text, "не трогает") {
		t.Errorf("о хранилище вне манифеста не сказано:\n%s", text)
	}
}
