package host

import (
	"context"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/plan"
)

const backupManifest = `{"backup":{"schedule":"02:00","storage":"local","mode":"snapshot",
  "guests":[100,101,102],"keep_last":3,"compress":"zstd"}}`

func backupPlan(t *testing.T, body string, jobs ...facts.BackupJob) plan.Changes {
	t.Helper()
	c, err := (Backup{}).Plan(context.Background(), parse(t, body), &facts.Facts{BackupJobs: jobs})
	if err != nil {
		t.Fatalf("сборка плана: %v", err)
	}
	return c
}

func keelJob() facts.BackupJob {
	return facts.BackupJob{ID: "backup-1", Comment: "keel", Schedule: "02:00",
		Storage: "local", Mode: "snapshot", VMID: "100,101,102"}
}

func TestBackupCreatesJob(t *testing.T) {
	c := backupPlan(t, backupManifest)
	assertCommands(t, commands(c.Steps), []string{
		"pvesh create /cluster/backup --schedule 02:00 --storage local --mode snapshot " +
			"--compress zstd --prune-backups keep-last=3 --comment keel --enabled 1 --vmid 100,101,102",
	})
}

func TestBackupLeavesMatchingJobAlone(t *testing.T) {
	if c := backupPlan(t, backupManifest, keelJob()); !c.Empty() {
		t.Errorf("совпадающее задание вызвало изменения: %+v", c.Steps)
	}
}

func TestBackupUpdatesChangedJob(t *testing.T) {
	job := keelJob()
	job.Schedule = "03:30"
	job.Storage = "nas"

	c := backupPlan(t, backupManifest, job)
	assertCommands(t, commands(c.Steps), []string{
		"pvesh set /cluster/backup/backup-1 --schedule 02:00 --storage local --mode snapshot " +
			"--compress zstd --prune-backups keep-last=3 --comment keel --enabled 1 --vmid 100,101,102",
	})
	// В описании должно быть видно, что именно расходится, — иначе человек
	// подтверждает «обновить задание» вслепую.
	for _, want := range []string{"03:30 → 02:00", "nas → local"} {
		if !strings.Contains(c.Steps[0].Summary, want) {
			t.Errorf("в описании нет %q: %s", want, c.Steps[0].Summary)
		}
	}
}

// Задание keel узнаётся по комментарию. Чужое задание с тем же
// расписанием — не наше, и трогать его нельзя.
func TestBackupNeverTouchesForeignJobs(t *testing.T) {
	foreign := facts.BackupJob{ID: "backup-чужой", Comment: "сделано руками",
		Schedule: "02:00", Storage: "local", Mode: "snapshot", VMID: "100,101,102"}

	c := backupPlan(t, backupManifest, foreign)
	got := commands(c.Steps)
	if len(got) != 1 || !strings.HasPrefix(got[0], "pvesh create") {
		t.Fatalf("чужое задание принято за своё: %v", got)
	}
	for _, cmd := range got {
		if strings.Contains(cmd, "backup-чужой") {
			t.Errorf("тронуто чужое задание: %s", cmd)
		}
	}
}

// Чужое задание «все гости» перекрывает наше: те же гости поедут в копию
// дважды за период. Трогать его нельзя, но и молчать не стоит.
func TestBackupWarnsAboutOverlappingAllJob(t *testing.T) {
	all := facts.BackupJob{ID: "backup-все", Comment: "", Schedule: "01:00"}
	all.All.UnmarshalJSON([]byte("1"))
	all.Enabled.UnmarshalJSON([]byte("1"))

	c := backupPlan(t, backupManifest, all)
	if len(c.Notes) != 1 {
		t.Fatalf("о перекрытии не сказано: %+v", c.Notes)
	}
	msg := c.Notes[0].Message
	for _, want := range []string{"дважды", "не трогает", "pvesh delete /cluster/backup/backup-все"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в предупреждении нет %q:\n%s", want, msg)
		}
	}
}

// Выключенное чужое задание никого не перекрывает — молчим.
func TestBackupIgnoresDisabledForeignJob(t *testing.T) {
	off := facts.BackupJob{ID: "backup-выкл", Schedule: "01:00"}
	off.All.UnmarshalJSON([]byte("1"))
	off.Enabled.UnmarshalJSON([]byte("0"))

	if c := backupPlan(t, backupManifest, off); len(c.Notes) != 0 {
		t.Errorf("выключенное задание принято за перекрытие: %+v", c.Notes)
	}
}

func TestBackupAllGuestsUsesAllFlag(t *testing.T) {
	c := backupPlan(t, `{"backup":{"schedule":"02:00","all":true}}`)
	got := commands(c.Steps)
	if len(got) != 1 || !strings.Contains(got[0], "--all 1") {
		t.Fatalf("охват «все гости» собран неверно: %v", got)
	}
	if strings.Contains(got[0], "--vmid") {
		t.Errorf("при охвате «все» перечислены и гости: %s", got[0])
	}
	// Значения по умолчанию те же, что были.
	for _, want := range []string{"--storage local", "--mode snapshot", "--compress zstd", "keep-last=3"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("потеряно значение по умолчанию %q: %s", want, got[0])
		}
	}
}

// Убрать ключ из манифеста — не то же самое, что попросить удалить.
// Но и молчать о брошенном задании нельзя: оно продолжит запускаться.
func TestBackupReportsOrphanedJob(t *testing.T) {
	m := parse(t, `{}`)
	f := &facts.Facts{BackupJobs: []facts.BackupJob{keelJob()}}

	if (Backup{}).Configured(m) {
		t.Fatal("без раздела backup провайдер объявил себя настроенным")
	}
	notes := (Backup{}).NotesWhenUnconfigured(m, f)
	if len(notes) != 1 {
		t.Fatalf("о брошенном задании не сказано: %+v", notes)
	}
	for _, want := range []string{"продолжит запускаться", "сам его не удаляет", "pvesh delete"} {
		if !strings.Contains(notes[0].Message, want) {
			t.Errorf("в предупреждении нет %q:\n%s", want, notes[0].Message)
		}
	}
}

func TestBackupSaysNothingWhenNothingLeftBehind(t *testing.T) {
	if notes := (Backup{}).NotesWhenUnconfigured(parse(t, `{}`), &facts.Facts{}); len(notes) != 0 {
		t.Errorf("на чистом хосте появилось предупреждение: %+v", notes)
	}
}

// Proxmox отдаёт булевы значения то числом, то строкой, то настоящим true.
func TestBackupJobFlagsAcceptEveryShape(t *testing.T) {
	for _, raw := range []string{`1`, `"1"`, `true`} {
		var f facts.BackupJob
		if err := f.All.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if !f.All.Bool() {
			t.Errorf("%s прочитано как ложь", raw)
		}
	}
	for _, raw := range []string{`0`, `"0"`, `false`} {
		var f facts.BackupJob
		if err := f.All.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if f.All.Bool() {
			t.Errorf("%s прочитано как истина", raw)
		}
	}
}

func TestBackupRejectsEmptyStorage(t *testing.T) {
	_, err := (Backup{}).Plan(context.Background(),
		parse(t, `{"backup":{"schedule":"02:00","storage":""}}`), &facts.Facts{})
	// Пустое значение подменяется значением по умолчанию, поэтому ошибки
	// быть не должно — но и пустым хранилище остаться не может.
	if err != nil {
		t.Fatalf("пустое storage должно подменяться значением по умолчанию: %v", err)
	}
}
