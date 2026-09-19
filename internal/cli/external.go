package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Каталог community-scripts — сотни готовых установщиков приложений в LXC.
// Втаскивать их в keel незачем: у них своя жизнь и свой темп релизов. Но
// и делать вид, что их нет, глупо.
const (
	extCatalog     = "https://community-scripts.github.io/ProxmoxVE/scripts"
	extPostInstall = "https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/tools/pve/post-pve-install.sh"
)

const extWarning = `Это чужие сценарии, не часть keel.

Что важно понимать:

  · выполняются с правами root и могут менять что угодно;
  · их содержимое может поменяться в любой момент — код скачивается
    из интернета прямо перед запуском;
  · результат НЕ описан в твоём манифесте. Значит при следующей сборке
    хоста с нуля он не восстановится, и помнить о нём придётся тебе.

Для разовой установки приложения в контейнер — удобно.
Для того, на чём держится хост, — опиши это в манифесте.`

// External запускает чужой сценарий, показав его перед этим.
//
// Скачивание и запуск — два разных решения, и между ними человек видит,
// что именно собирается выполнить. Это единственная защита, которая
// здесь вообще возможна: код чужой и меняется без предупреждения.
func External(ctx context.Context, a *App, url string) error {
	if err := NeedRoot(); err != nil {
		return err
	}

	s := newSheet(a.Out)
	s.section("Внешние инструменты")
	fmt.Fprintln(a.Out, extWarning)
	fmt.Fprintf(a.Out, "\nКаталог: %s\n", extCatalog)

	switch url {
	case "":
		s.section("Как запустить")
		s.row("post-pve-install", "keel external post")
		s.row("свой сценарий", "keel external ССЫЛКА")
		return nil
	case "post":
		url = extPostInstall
	}
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("ссылка должна начинаться с https:// — по http код приедет неизвестно от кого")
	}

	client := &http.Client{Timeout: time.Minute}
	body, err := fetchBytes(ctx, client, url)
	if err != nil {
		return fmt.Errorf("не удалось скачать сценарий: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("скачанный файл пуст — проверь ссылку")
	}

	name := url[strings.LastIndexByte(url, '/')+1:]
	lines := strings.Split(string(body), "\n")

	s.section(fmt.Sprintf("Начало %s (%s)", name, plural(len(lines), "строка", "строки", "строк")))
	for i, line := range lines {
		if i >= 40 {
			s.note(fmt.Sprintf("… и ещё %d", len(lines)-40))
			break
		}
		fmt.Fprintf(a.Out, "  %s\n", line)
	}

	// Сценарий кладётся в каталог keel, а не в /tmp: он выполняется с
	// правами root, и человек должен иметь возможность посмотреть, что
	// именно выполнялось, уже после запуска.
	dir := filepath.Join(a.Paths.Home(), "external")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02_150405")+"-"+name)
	if err := os.WriteFile(path, body, 0o700); err != nil {
		return err
	}

	runner := &exec.Runner{
		Out: a.Out, Log: a.Log, Mask: a.Masker.Apply,
		DryRun: a.Opts.Mode == ModeDry,
	}
	if a.Opts.Mode == ModeStep {
		runner.Confirm = askAboutStep(a)
	}
	step := plan.Step{
		ID: "external:" + name, Provider: "external", Resource: name,
		Summary: "выполнить чужой сценарий " + name + " с правами root",
		Action:  plan.ActionExec, Cmd: []string{"bash", path},
		// Сценарий может спросить что угодно: он не наш и о keel не знает.
		Interactive: true,
		Unknown:     []string{"что именно он сделает — решает его автор"},
	}
	if err := runner.Do(ctx, step); err != nil {
		return err
	}
	s.row("сценарий сохранён", path)
	s.warn("вне манифеста", "при сборке хоста с нуля это не восстановится")
	return nil
}
