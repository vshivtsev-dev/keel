package facts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vshivtsev-dev/keel/internal/exec"
)

// buildReport собирает текстовый снимок хоста.
//
// Это то, чего нет ни в одном конфиге, но чего отчаянно не хватает, когда
// всё сгорело: гости-то восстановятся из vzdump, а какого размера были
// диски, как называется мост и что за версия PVE стояла — уже нет.
//
// Отчёт нарочно текстовый и человекочитаемый: его будут читать в тот
// момент, когда никакого keel под рукой может и не быть.
func buildReport(ctx context.Context, c exec.Capturer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Снимок хоста, сделан %s\n", time.Now().Format("2006-01-02 15:04:05"))

	sections := []struct {
		title    string
		cmd      []string
		fallback string
	}{
		{"Версия Proxmox", []string{"pveversion", "-v"}, "pveversion недоступен"},
		{"Диски", []string{"lsblk", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT"}, ""},
		{"Хранилища", []string{"pvesm", "status"}, ""},
		{"Сеть", []string{"ip", "-o", "addr", "show"}, ""},
		{"Виртуальные машины", []string{"qm", "list"}, ""},
		{"Контейнеры", []string{"pct", "list"}, ""},
		{"ZFS", []string{"zpool", "list"}, "ZFS не используется"},
		{"Загрузчик", []string{"proxmox-boot-tool", "status"}, "proxmox-boot-tool недоступен"},
	}

	for _, s := range sections {
		fmt.Fprintf(&b, "\n## %s\n", s.title)
		out := ""
		if c.Has(s.cmd[0]) {
			if got, err := c.Capture(ctx, s.cmd[0], s.cmd[1:]...); err == nil {
				out = strings.TrimRight(got, "\n")
			}
		}
		if out == "" {
			out = s.fallback
		}
		if out != "" {
			b.WriteString(out + "\n")
		}
	}
	return b.String()
}
