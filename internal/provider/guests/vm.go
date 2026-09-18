package guests

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/image"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/profile"
	"github.com/vshivtsev-dev/keel/internal/ru"
	"github.com/vshivtsev-dev/keel/internal/secret"
)

// vmBaseArgs — общая часть qm create для обоих видов машин.
func vmBaseArgs(want manifest.Guest, p *profile.Profile) []string {
	args := []string{
		"--name", want.Name,
		"--memory", strconv.Itoa(memory(want, p)),
		"--cores", strconv.Itoa(cores(want, p)),
		"--cpu", p.VM.CPUOr(),
		"--machine", p.VM.MachineOr(),
		"--bios", p.VM.BIOSOr(),
		"--scsihw", p.VM.SCSIHWOr(),
		"--ostype", p.VM.OSTypeOr(),
		"--net0", "virtio,bridge=" + bridge(want),
	}
	if p.VM.Agent {
		args = append(args, "--agent", "1")
	}
	if onBoot(want) {
		args = append(args, "--onboot", "1")
	}
	if p.VM.VGA != "" {
		args = append(args, "--vga", p.VM.VGA)
	}
	if p.VM.Audio != "" && p.VM.Audio != "false" {
		args = append(args, "--audio0", "device="+p.VM.Audio)
	}
	if p.VM.Serial {
		args = append(args, "--serial0", "socket")
	}
	return args
}

// imageSteps скачивает образ и распаковывает его, если он ещё не в кэше.
// Возвращает шаги и путь к готовому файлу.
func (g Guests) imageSteps(ctx context.Context, want manifest.Guest, p *profile.Profile) ([]plan.Step, string, error) {
	res, err := g.Images.Resolve(ctx, p, want.ImageVersion)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", guestName(want), err)
	}
	archiveName, imageName := image.LocalName(res.URL, res.Compressed)
	archive := path.Join(g.CacheDir, archiveName)
	imagePath := path.Join(g.CacheDir, imageName)

	id := strconv.Itoa(want.ID)
	mkdirID := "guests:" + id + ":cache"
	steps := []plan.Step{{
		ID: mkdirID, Provider: "guests", Resource: guestName(want),
		Summary: "создать кэш образов " + g.CacheDir,
		Action:  plan.ActionMkdir, Path: g.CacheDir,
	}}

	downloadID := "guests:" + id + ":download"
	summary := "скачать образ " + archiveName
	if res.Fallback {
		summary += fmt.Sprintf(" (версии %s по ожидаемому адресу нет — беру запасную)", res.Version)
	}
	steps = append(steps, plan.Step{
		ID: downloadID, Provider: "guests", Resource: guestName(want),
		Summary: summary,
		Action:  plan.ActionExec,
		Cmd:     []string{"curl", "-fL", "--progress-bar", "-o", archive, res.URL},
		Needs:   []string{mkdirID},
		Unknown: []string{"размер образа станет известен при закачке"},
	})

	switch res.Compressed {
	case "xz":
		steps = append(steps, plan.Step{
			ID: "guests:" + id + ":unpack", Provider: "guests", Resource: guestName(want),
			Summary: "распаковать " + archiveName,
			Action:  plan.ActionExec, Cmd: []string{"xz", "-d", "-k", archive},
			Needs: []string{downloadID},
		})
	case "gz":
		steps = append(steps, plan.Step{
			ID: "guests:" + id + ":unpack", Provider: "guests", Resource: guestName(want),
			Summary: "распаковать " + archiveName,
			Action:  plan.ActionExec, Cmd: []string{"gunzip", "-k", archive},
			Needs: []string{downloadID},
		})
	}
	return steps, imagePath, nil
}

// planVMFromImage — машина из готового образа, как Home Assistant OS.
func (g Guests) planVMFromImage(ctx context.Context, want manifest.Guest, p *profile.Profile, _ *facts.Facts) ([]plan.Step, error) {
	steps, imagePath, err := g.imageSteps(ctx, want, p)
	if err != nil {
		return nil, err
	}
	id := strconv.Itoa(want.ID)
	needs := []string{steps[len(steps)-1].ID}

	createID := "guests:" + id + ":create"
	steps = append(steps, plan.Step{
		ID: createID, Provider: "guests", Resource: guestName(want),
		Summary: fmt.Sprintf("создать ВМ %s из образа (%s): %s, %d МБ, диск %s, хранилище %s",
			id, p.Title, ru.Cores(cores(want, p)), memory(want, p), orDash(disk(want, p)), storage(want)),
		Action: plan.ActionExec,
		Cmd:    append([]string{"qm", "create", id}, vmBaseArgs(want, p)...),
		Needs:  needs,
	})

	steps = append(steps, g.vmDiskSteps(want, p, id, imagePath, createID)...)
	return append(steps, g.vmTailSteps(want, p, id, "guests:"+id+":boot")...), nil
}

// vmDiskSteps — общая для обоих видов машин часть: EFI-диск, импорт
// образа, порядок загрузки, расширение диска, запуск.
func (g Guests) vmDiskSteps(want manifest.Guest, p *profile.Profile, id, imagePath, createID string) []plan.Step {
	var steps []plan.Step
	prev := createID

	if p.VM.EFIDisk {
		keys := "0"
		if p.VM.PreEnrolledKeys {
			keys = "1"
		}
		efiID := "guests:" + id + ":efi"
		steps = append(steps, plan.Step{
			ID: efiID, Provider: "guests", Resource: guestName(want),
			Summary: "добавить EFI-диск",
			Action:  plan.ActionExec,
			Cmd: []string{"qm", "set", id, "--efidisk0",
				storage(want) + ":0,efitype=4m,pre-enrolled-keys=" + keys},
			Needs: []string{prev},
		})
		prev = efiID
	}

	// import-from создаёт диск и заливает в него образ одной командой:
	// не нужно разбирать вывод импорта, чтобы узнать имя тома.
	importID := "guests:" + id + ":import"
	steps = append(steps, plan.Step{
		ID: importID, Provider: "guests", Resource: guestName(want),
		Summary: "импортировать образ в диск ВМ",
		Action:  plan.ActionExec,
		Cmd: []string{"qm", "set", id, "--scsi0",
			storage(want) + ":0,import-from=" + imagePath + ",discard=on,ssd=1"},
		Needs: []string{prev},
	})
	return append(steps, plan.Step{
		ID: "guests:" + id + ":boot", Provider: "guests", Resource: guestName(want),
		Summary: "настроить порядок загрузки",
		Action:  plan.ActionExec, Cmd: []string{"qm", "set", id, "--boot", "order=scsi0"},
		Needs: []string{importID},
	})
}

func (g Guests) vmTailSteps(want manifest.Guest, p *profile.Profile, id, after string) []plan.Step {
	var steps []plan.Step
	prev := after

	if d := disk(want, p); d != "" {
		resizeID := "guests:" + id + ":resize"
		steps = append(steps, plan.Step{
			ID: resizeID, Provider: "guests", Resource: guestName(want),
			Summary: "расширить диск до " + d,
			Action:  plan.ActionExec, Cmd: []string{"qm", "resize", id, "scsi0", d},
			Needs: []string{prev},
			// Образ бывает уже больше запрошенного, и тогда qm откажется.
			// Это не повод считать ВМ несозданной.
			Unknown: []string{"расширение не сработает, если образ уже больше " + d},
		})
		prev = resizeID
	}
	if startNow(want) {
		steps = append(steps, plan.Step{
			ID: "guests:" + id + ":start", Provider: "guests", Resource: guestName(want),
			Summary: "запустить ВМ " + id,
			Action:  plan.ActionExec, Cmd: []string{"qm", "start", id},
			Needs: []string{prev},
		})
	}
	return steps
}

// planVMCloudInit — машина из облачного образа, настраиваемая cloud-init
// до первого старта.
func (g Guests) planVMCloudInit(ctx context.Context, want manifest.Guest, p *profile.Profile, f *facts.Facts) ([]plan.Step, error) {
	snip := f.SnippetStorage()
	if snip == nil {
		return nil, fmt.Errorf("%s: ни одному хранилищу не разрешены сниппеты, а без них cloud-init не настроить.\n"+
			"Добавь \"snippets\" в content хранилища local — это сделает провайдер host/storage",
			guestName(want))
	}
	if snip.Path == "" {
		return nil, fmt.Errorf("%s: не удалось узнать каталог хранилища %s", guestName(want), snip.Name)
	}

	steps, imagePath, err := g.imageSteps(ctx, want, p)
	if err != nil {
		return nil, err
	}
	id := strconv.Itoa(want.ID)
	createID := "guests:" + id + ":create"

	steps = append(steps, plan.Step{
		ID: createID, Provider: "guests", Resource: guestName(want),
		Summary: fmt.Sprintf("создать ВМ %s из облачного образа (%s): %s, %d МБ, диск %s, хранилище %s",
			id, p.Title, ru.Cores(cores(want, p)), memory(want, p), orDash(disk(want, p)), storage(want)),
		Action: plan.ActionExec,
		Cmd:    append([]string{"qm", "create", id}, vmBaseArgs(want, p)...),
		Needs:  []string{steps[len(steps)-1].ID},
	})

	steps = append(steps, g.vmDiskSteps(want, p, id, imagePath, createID)...)
	bootID := "guests:" + id + ":boot"

	steps = append(steps, plan.Step{
		ID: "guests:" + id + ":cidisk", Provider: "guests", Resource: guestName(want),
		Summary: "подключить диск cloud-init",
		Action:  plan.ActionExec,
		Cmd:     []string{"qm", "set", id, "--ide2", storage(want) + ":cloudinit"},
		Needs:   []string{bootID},
	})

	// Раз уж мы отдаём cicustom, штатные ciuser и sshkeys игнорируются —
	// значит пользователя и ключи описываем в user-data сами.
	snipFile := "keel-" + id + "-user.yml"
	userData, refs := cloudInitUserData(want, p)
	writeID := "guests:" + id + ":cloudinit"
	steps = append(steps, plan.Step{
		ID: writeID, Provider: "guests", Resource: guestName(want),
		Summary:    "записать настройки первого запуска (cloud-init)",
		Action:     plan.ActionWrite,
		Path:       path.Join(snip.Path, "snippets", snipFile),
		Content:    []byte(userData),
		SecretRefs: refs,
		Needs:      []string{"guests:" + id + ":cidisk"},
	})

	steps = append(steps, plan.Step{
		ID: "guests:" + id + ":cicustom", Provider: "guests", Resource: guestName(want),
		Summary: "указать ВМ файл настроек первого запуска",
		Action:  plan.ActionExec,
		Cmd:     []string{"qm", "set", id, "--cicustom", "user=" + snip.Name + ":snippets/" + snipFile},
		Needs:   []string{writeID},
	})

	ipcfg := "ip=dhcp"
	if want.CloudInit != nil && want.CloudInit.IPConfig != "" {
		ipcfg = want.CloudInit.IPConfig
	}
	netID := "guests:" + id + ":net"
	steps = append(steps, plan.Step{
		ID: netID, Provider: "guests", Resource: guestName(want),
		Summary: "настроить сеть гостя (" + ipcfg + ")",
		Action:  plan.ActionExec, Cmd: []string{"qm", "set", id, "--ipconfig0", ipcfg},
		Needs: []string{"guests:" + id + ":cicustom"},
	})

	return append(steps, g.vmTailSteps(want, p, id, netID)...), nil
}

// cloudInitUserData собирает user-data. Пароль в него попадает меткой:
// сам файл сохраняется в плане, а секретов в плане быть не может.
func cloudInitUserData(want manifest.Guest, p *profile.Profile) (string, []string) {
	var b strings.Builder
	var refs []string

	user := "admin"
	if want.CloudInit != nil && want.CloudInit.User != "" {
		user = want.CloudInit.User
	}

	fmt.Fprintf(&b, "#cloud-config\n# Создано keel для гостя %s\n", want.Name)
	fmt.Fprintf(&b, "hostname: %s\n", want.Name)
	b.WriteString("manage_etc_hosts: true\n")
	b.WriteString("users:\n")
	fmt.Fprintf(&b, "  - name: %s\n", user)
	b.WriteString("    groups: [adm, sudo, video, render, audio]\n")
	b.WriteString("    shell: /bin/bash\n")
	b.WriteString("    sudo: [\"ALL=(ALL) NOPASSWD:ALL\"]\n")

	if want.CloudInit != nil && len(want.CloudInit.SSHKeys) > 0 {
		b.WriteString("    ssh_authorized_keys:\n")
		for _, k := range want.CloudInit.SSHKeys {
			fmt.Fprintf(&b, "      - %s\n", k)
		}
	}
	if p.NeedsPassword {
		ref := strconv.Itoa(want.ID)
		refs = append(refs, ref)
		b.WriteString("    lock_passwd: false\n")
		fmt.Fprintf(&b, "    plain_text_passwd: %s\n", secret.Mark(ref))
	}

	b.WriteString("package_update: true\n")
	if pkgs := append(append([]string{}, p.Packages...), want.Packages...); len(pkgs) > 0 {
		b.WriteString("packages:\n")
		for _, pkg := range pkgs {
			fmt.Fprintf(&b, "  - %s\n", pkg)
		}
	}
	if len(p.Runcmd) > 0 {
		b.WriteString("runcmd:\n")
		for _, cmd := range p.Runcmd {
			fmt.Fprintf(&b, "  - %s\n", cmd)
		}
	}
	return b.String(), refs
}
