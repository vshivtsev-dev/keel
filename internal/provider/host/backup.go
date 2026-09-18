package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

// Backup заводит задание vzdump по расписанию из манифеста.
//
// Порт modules/host/40-backup-jobs.sh. Задание помечается комментарием
// keel — по нему оно и находится при повторных запусках. Чужие задания
// не трогаются никогда.
type Backup struct{}

// backupTag — метка, по которой keel узнаёт своё задание. Менять её нельзя:
// после смены keel перестанет видеть заведённое раньше и заведёт второе.
const backupTag = "keel"

func (Backup) ID() string    { return "host/backup" }
func (Backup) Title() string { return "Резервное копирование гостей" }

func (Backup) Describe() string {
	return `Заводит задание vzdump по расписанию из манифеста и поддерживает его в
описанном состоянии. Задание помечается комментарием keel; задания,
созданные вручную или другими инструментами, не трогаются.`
}

func (Backup) Configured(m *manifest.Manifest) bool {
	return m.Backup != nil && m.Backup.Schedule != ""
}

func (b Backup) Plan(_ context.Context, m *manifest.Manifest, f *facts.Facts) (plan.Changes, error) {
	want := m.Backup
	if want.StorageOr() == "" {
		return plan.Changes{}, fmt.Errorf("backup.storage не может быть пустым")
	}

	var out plan.Changes

	// Предупреждение имеет смысл, только если наше задание адресное:
	// два задания «все гости» — это просто два задания.
	if !coversAll(want) {
		out.Notes = append(out.Notes, overlapNotes(f)...)
	}

	args := backupArgs(want)
	have := f.BackupJobBy(backupTag)

	if have == nil {
		out.Steps = append(out.Steps, plan.Step{
			ID:       b.ID() + ":create",
			Provider: b.ID(),
			Resource: "задание vzdump",
			Summary:  fmt.Sprintf("создать задание: %s, хранилище %s", want.Schedule, want.StorageOr()),
			Action:   plan.ActionExec,
			Cmd:      append([]string{"pvesh", "create", "/cluster/backup"}, args...),
		})
		return out, nil
	}

	diffs := backupDiff(want, have)
	if len(diffs) == 0 {
		return out, nil
	}
	out.Steps = append(out.Steps, plan.Step{
		ID:       b.ID() + ":update",
		Provider: b.ID(),
		Resource: "задание vzdump " + have.ID,
		Summary:  "обновить задание: " + strings.Join(diffs, "; "),
		Action:   plan.ActionExec,
		Cmd:      append([]string{"pvesh", "set", "/cluster/backup/" + have.ID}, args...),
	})
	return out, nil
}

func (b Backup) Verify(_ context.Context, m *manifest.Manifest, f *facts.Facts) ([]plan.Finding, error) {
	want := m.Backup
	have := f.BackupJobBy(backupTag)
	if have == nil {
		return []plan.Finding{{Provider: b.ID(), Resource: "задание vzdump",
			Message: "задания нет"}}, nil
	}
	if diffs := backupDiff(want, have); len(diffs) > 0 {
		return []plan.Finding{{Provider: b.ID(), Resource: "задание vzdump " + have.ID,
			Message: strings.Join(diffs, "; ")}}, nil
	}
	return []plan.Finding{{Provider: b.ID(), Resource: "задание vzdump " + have.ID,
		Message: fmt.Sprintf("на месте: %s, хранилище %s", want.Schedule, want.StorageOr()),
		OK:      true}}, nil
}

// NotesWhenUnconfigured рассказывает о брошенном задании.
//
// Раздел backup убрали из манифеста, а задание keel на хосте осталось.
// Правило нуля запрещает молча удалять: убрать ключ из манифеста — не то
// же самое, что попросить удалить. Но и молчать нельзя — задание
// продолжит запускаться по расписанию.
func (b Backup) NotesWhenUnconfigured(_ *manifest.Manifest, f *facts.Facts) []plan.Note {
	have := f.BackupJobBy(backupTag)
	if have == nil {
		return nil
	}
	return []plan.Note{{
		Resource: "задание vzdump " + have.ID,
		Message: "в манифесте нет раздела backup, а задание keel на хосте осталось.\n" +
			"Оно продолжит запускаться по расписанию. keel сам его не удаляет: " +
			"убрать ключ из манифеста — не то же самое, что попросить удалить.\n" +
			howToRemove(have.ID),
	}}
}

// overlapNotes ищет чужое задание «все гости»: оно перекрывает наше, и те
// же гости поедут в копию дважды за период. Трогать его нельзя — чужое, —
// но и молчать об этом не стоит.
func overlapNotes(f *facts.Facts) []plan.Note {
	var out []plan.Note
	for _, job := range f.BackupJobs {
		if job.Comment == backupTag || !job.All.Bool() || !job.Enabled.Bool() {
			continue
		}
		out = append(out, plan.Note{
			Resource: "чужое задание " + job.ID,
			Message: fmt.Sprintf("на хосте есть чужое задание «все гости» (%s).\n"+
				"Гости из манифеста попадут в копию и по нему — дважды за период.\n"+
				"keel чужие задания не трогает — это решение за тобой.\n%s",
				orDash(job.Schedule), howToRemove(job.ID)),
		})
	}
	return out
}

// howToRemove печатается вместе с любым предупреждением о заданиях:
// назвать проблему и не дать выхода — хуже, чем промолчать.
func howToRemove(id string) string {
	return "Убрать: Центр обработки данных → Резервная копия → выбрать → Удалить\n" +
		"или командой: pvesh delete /cluster/backup/" + id
}

func coversAll(want *manifest.Backup) bool {
	if want.All != nil && *want.All {
		return true
	}
	return len(want.Guests) == 0
}

func vmidList(want *manifest.Backup) string {
	ids := make([]string, 0, len(want.Guests))
	for _, id := range want.Guests {
		ids = append(ids, strconv.Itoa(id))
	}
	return strings.Join(ids, ",")
}

// backupArgs собирает аргументы задания — одно место на сборку плана и
// на проверку.
func backupArgs(want *manifest.Backup) []string {
	args := []string{
		"--schedule", want.Schedule,
		"--storage", want.StorageOr(),
		"--mode", want.ModeOr(),
		"--compress", want.CompressOr(),
		"--prune-backups", "keep-last=" + strconv.Itoa(want.KeepLastOr()),
		"--comment", backupTag,
		"--enabled", "1",
	}
	if coversAll(want) {
		return append(args, "--all", "1")
	}
	return append(args, "--vmid", vmidList(want))
}

// backupDiff — чем существующее задание отличается от описанного.
func backupDiff(want *manifest.Backup, have *facts.BackupJob) []string {
	var out []string
	add := func(what, from, to string) {
		if from != to {
			out = append(out, fmt.Sprintf("%s: %s → %s", what, orDash(from), to))
		}
	}
	add("расписание", have.Schedule, want.Schedule)
	add("хранилище", have.Storage, want.StorageOr())
	add("режим", have.Mode, want.ModeOr())

	if coversAll(want) {
		if !have.All.Bool() {
			out = append(out, "охват: → все гости")
		}
		return out
	}
	add("гости", have.VMID, vmidList(want))
	return out
}
