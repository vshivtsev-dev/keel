package guests

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/profile"
	"github.com/vshivtsev-dev/keel/internal/ru"
	"github.com/vshivtsev-dev/keel/internal/secret"
)

func (g Guests) planLXC(want manifest.Guest, p *profile.Profile, f *facts.Facts) ([]plan.Step, error) {
	if p.Template == nil || p.Template.Pattern == "" {
		return nil, fmt.Errorf("%s: в профиле %s не указан шаблон контейнера", guestName(want), p.Name)
	}
	id := strconv.Itoa(want.ID)
	tmplStore := p.Template.StorageOr()

	var steps []plan.Step
	var prev string

	// Скачивать заново то, что уже лежит на диске, — минуты ожидания на
	// ровном месте.
	template, downloaded := f.Template(p.Template.Pattern)
	switch {
	case downloaded:
		// Ничего делать не нужно.
	case template != "":
		updateID := "guests:" + id + ":pveam-update"
		steps = append(steps, plan.Step{
			ID: updateID, Provider: "guests", Resource: guestName(want),
			Summary: "обновить список шаблонов контейнеров",
			Action:  plan.ActionExec, Cmd: []string{"pveam", "update"},
		})
		prev = "guests:" + id + ":pveam-download"
		steps = append(steps, plan.Step{
			ID: prev, Provider: "guests", Resource: guestName(want),
			Summary: "скачать шаблон " + template,
			Action:  plan.ActionExec, Cmd: []string{"pveam", "download", tmplStore, template},
			Needs: []string{updateID},
		})
	default:
		return nil, fmt.Errorf("%s: среди доступных шаблонов нет подходящего под «%s».\n"+
			"Обнови список: pveam update", guestName(want), p.Template.Pattern)
	}

	args := []string{
		"--hostname", want.Name,
		"--cores", strconv.Itoa(cores(want, p)),
		"--memory", strconv.Itoa(memory(want, p)),
		"--swap", strconv.Itoa(p.LXC.SwapOr()),
		"--rootfs", fmt.Sprintf("%s:%d", storage(want), diskToGB(orDefault(disk(want, p), "32G"))),
		"--net0", fmt.Sprintf("name=eth0,bridge=%s,ip=%s", bridge(want), orDefault(want.IP, "dhcp")),
		"--ostype", p.LXC.OSTypeOr(),
	}
	if p.LXC.UnprivilegedOr() {
		args = append(args, "--unprivileged", "1")
	}
	if p.LXC.Features != "" {
		args = append(args, "--features", p.LXC.Features)
	}
	if onBoot(want) {
		args = append(args, "--onboot", "1")
	}

	createID := "guests:" + id + ":create"
	createStep := plan.Step{
		ID: createID, Provider: "guests", Resource: guestName(want),
		Summary: fmt.Sprintf("создать контейнер %s (%s): %s, %d МБ, диск %s, хранилище %s",
			id, p.Title, ru.Cores(cores(want, p)), memory(want, p), orDash(disk(want, p)), storage(want)),
		Action: plan.ActionExec,
		Cmd: append([]string{"pct", "create", id,
			tmplStore + ":vztmpl/" + template}, args...),
	}
	if prev != "" {
		createStep.Needs = []string{prev}
	}
	steps = append(steps, createStep)
	prev = createID

	if p.LXC.DRI {
		dri, err := driSteps(want, p, f, id, prev)
		if err != nil {
			return nil, err
		}
		if len(dri) > 0 {
			steps = append(steps, dri...)
			prev = dri[len(dri)-1].ID
		}
	}

	startID := "guests:" + id + ":start"
	steps = append(steps, plan.Step{
		ID: startID, Provider: "guests", Resource: guestName(want),
		Summary: "запустить контейнер " + id,
		Action:  plan.ActionExec, Cmd: []string{"pct", "start", id},
		Needs: []string{prev},
	})

	return append(steps, postInstallSteps(want, p, id, startID)...), nil
}

// driSteps отдаёт контейнеру видеокарту хоста.
func driSteps(want manifest.Guest, p *profile.Profile, f *facts.Facts, id, after string) ([]plan.Step, error) {
	if len(f.DRINodes) == 0 {
		return nil, fmt.Errorf("%s: на хосте нет /dev/dri — видеокарту в контейнер не отдать.\n"+
			"Проверь keel doctor: драйвер хоста должен видеть видеокарту", guestName(want))
	}
	// Ключи dev0..devN появились в PVE 8.2. Делать вид, что команда
	// сработает на более старом хосте, нельзя: способ для старых версий
	// описан в docs/30-desktop.md.
	if !f.PctDevKeys {
		return nil, fmt.Errorf("%s: этот Proxmox не поддерживает ключи dev0 (нужен PVE 8.2+).\n"+
			"Способ для старых версий описан в docs/30-desktop.md", guestName(want))
	}

	var steps []plan.Step
	prev := after
	n := 0
	add := func(node string, gid int) {
		stepID := fmt.Sprintf("guests:%s:dev%d", id, n)
		steps = append(steps, plan.Step{
			ID: stepID, Provider: "guests", Resource: guestName(want),
			Summary: "отдать контейнеру " + node,
			Action:  plan.ActionExec,
			Cmd:     []string{"pct", "set", id, fmt.Sprintf("--dev%d", n), fmt.Sprintf("%s,gid=%d", node, gid)},
			Needs:   []string{prev},
		})
		prev = stepID
		n++
	}
	if node := f.RenderNode(); node != "" {
		add(node, p.LXC.RenderGIDOr())
	}
	if node := f.CardNode(); node != "" {
		add(node, p.LXC.VideoGIDOr())
	}
	return steps, nil
}

// postInstallSteps — первичная настройка внутри контейнера.
//
// Сценарий уносит с собой пароль и токен, поэтому кладётся с правами 700 и
// последней строкой удаляет сам себя: открытый дескриптор у bash остаётся,
// дочитать себя он успеет.
func postInstallSteps(want manifest.Guest, p *profile.Profile, id, after string) []plan.Step {
	user := "admin"
	if want.CloudInit != nil && want.CloudInit.User != "" {
		user = want.CloudInit.User
	}

	script, refs := postInstallScript(want, p, user)
	scriptPath := "/root/keel-post-install.sh"

	pushID := "guests:" + id + ":push"
	steps := []plan.Step{{
		ID: pushID, Provider: "guests", Resource: guestName(want),
		Summary: "загрузить сценарий настройки в контейнер",
		Action:  plan.ActionExec,
		// Сценарий передаётся через stdin контейнера: писать его во
		// временный файл на хосте значило бы оставить пароль лежать
		// на диске, пусть и ненадолго.
		Cmd: []string{"pct", "exec", id, "--", "bash", "-c",
			"cat > " + scriptPath + " <<'KEEL_EOF'\n" + script + "\nKEEL_EOF\nchmod 700 " + scriptPath},
		SecretRefs: refs,
		Needs:      []string{after},
	}}

	steps = append(steps, plan.Step{
		ID: "guests:" + id + ":post-install", Provider: "guests", Resource: guestName(want),
		Summary: "выполнить первичную настройку внутри контейнера",
		Action:  plan.ActionExec,
		Cmd:     []string{"pct", "exec", id, "--", "bash", scriptPath},
		Needs:   []string{pushID},
		Unknown: []string{"что именно поставится внутри — решит apt в контейнере"},
	})
	return steps
}

func postInstallScript(want manifest.Guest, p *profile.Profile, user string) (string, []string) {
	var b strings.Builder
	var refs []string

	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Создано keel. Первичная настройка контейнера.\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("export DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("apt-get update\n")

	if pkgs := append(append([]string{}, p.Packages...), want.Packages...); len(pkgs) > 0 {
		fmt.Fprintf(&b, "apt-get install -y --no-install-recommends %s\n", strings.Join(pkgs, " "))
	}

	// Пользователь заводится только там, где в него будут входить.
	// Служебному контейнеру вроде туннеля он не нужен, и создавать его
	// «на всякий случай» значит оставлять лишнюю учётную запись с паролем.
	if p.NeedsPassword {
		ref := strconv.Itoa(want.ID)
		refs = append(refs, ref)
		fmt.Fprintf(&b, "id -u %s >/dev/null 2>&1 || adduser --disabled-password --gecos \"\" %s\n", user, user)
		fmt.Fprintf(&b, "printf '%%s:%%s' '%s' '%s' | chpasswd\n", user, secret.Mark(ref))
		fmt.Fprintf(&b, "for g in sudo video render audio; do getent group \"$g\" >/dev/null && adduser %s \"$g\" || true; done\n", user)
	}

	// Права на видеокарту: вместо того чтобы угадывать gid (в Ubuntu
	// render=993, в Debian 104, и это меняется), смотрим, какой группе
	// устройство досталось на самом деле, и добавляем пользователя
	// именно в неё.
	if p.LXC.DRI {
		b.WriteString(`if [ -d /dev/dri ]; then
  for dev in /dev/dri/*; do
    [ -e "$dev" ] || continue
    gid=$(stat -c %g "$dev")
    grp=$(getent group "$gid" | cut -d: -f1)
    if [ -z "$grp" ]; then
      grp="keel-dri${gid}"
      groupadd -g "$gid" "$grp" 2>/dev/null || true
    fi
    adduser ` + user + ` "$grp" 2>/dev/null || true
  done
fi
`)
	}

	for _, cmd := range append(append([]string{}, p.Runcmd...), want.Runcmd...) {
		b.WriteString(cmd + "\n")
	}

	// Что делать с токеном, знает профиль, а не keel: здесь только
	// подстановка. Идёт последней, после runcmd: к этому моменту нужная
	// программа уже установлена.
	if p.TokenCommand != "" {
		ref := p.TokenFile
		if ref == "" {
			ref = p.Name
		}
		refs = append(refs, ref)
		b.WriteString(strings.ReplaceAll(p.TokenCommand, "KEEL_TOKEN", secret.Mark(ref)) + "\n")
	}

	b.WriteString("rm -f \"$0\"\n")
	return b.String(), refs
}
