package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/provider"
)

// stub — провайдер, который отдаёт заранее заданный ответ. Настоящие
// провайдеры проверяются своими тестами; здесь проверяется движок.
type stub struct {
	id         string
	configured bool
	changes    plan.Changes
	err        error
	findings   []plan.Finding
}

func (s stub) ID() string       { return s.id }
func (s stub) Title() string    { return "заглушка " + s.id }
func (s stub) Describe() string { return "" }

func (s stub) Configured(*manifest.Manifest) bool { return s.configured }

func (s stub) Plan(context.Context, *manifest.Manifest, *facts.Facts) (plan.Changes, error) {
	return s.changes, s.err
}

func (s stub) Verify(context.Context, *manifest.Manifest, *facts.Facts) ([]plan.Finding, error) {
	return s.findings, s.err
}

func step(id string, cmd ...string) plan.Step {
	return plan.Step{ID: id, Provider: "p", Summary: id, Action: plan.ActionExec, Cmd: cmd}
}

func statusOf(results []Result, id string) Status {
	for _, r := range results {
		if r.Provider == id {
			return r.Status
		}
	}
	return "нет такого"
}

// Правило нуля должно быть видно отдельно от «уже в порядке»: первое значит
// «в манифесте про это не сказано», второе — «сказано, и всё совпадает».
// Слить их вместе значило бы скрыть от человека, что он забыл ключ.
func TestCollectTellsUnconfiguredFromOK(t *testing.T) {
	reg := provider.NewRegistry(
		stub{id: "нет-в-манифесте", configured: false},
		stub{id: "всё-совпало", configured: true},
		stub{id: "есть-изменения", configured: true,
			changes: plan.Changes{Steps: []plan.Step{step("s1", "true")}}},
		stub{id: "не-разобрался", configured: true, err: os.ErrPermission},
	)
	m := &manifest.Manifest{}
	p, results := New(reg, nil, "тест").Collect(context.Background(), m, &facts.Facts{})

	if got := statusOf(results, "нет-в-манифесте"); got != StatusUnconfigured {
		t.Errorf("правило нуля: %s", got)
	}
	if got := statusOf(results, "всё-совпало"); got != StatusOK {
		t.Errorf("совпадающее состояние: %s", got)
	}
	if got := statusOf(results, "есть-изменения"); got != StatusChanges {
		t.Errorf("изменения: %s", got)
	}
	if got := statusOf(results, "не-разобрался"); got != StatusFailed {
		t.Errorf("ошибка провайдера: %s", got)
	}

	// В план попадают шаги только того провайдера, у которого они есть.
	if len(p.Steps) != 1 || p.Steps[0].ID != "s1" {
		t.Errorf("в план попало лишнее: %+v", p.Steps)
	}
}

// Заметка — это не изменение. Провайдер, у которого есть только заметки,
// ничего делать не собирается, и план не должен обещать обратного.
func TestCollectDoesNotCountNotesAsChanges(t *testing.T) {
	reg := provider.NewRegistry(stub{id: "p", configured: true,
		changes: plan.Changes{Notes: []plan.Note{{Resource: "tank", Message: "сам заведи"}}}})

	p, results := New(reg, nil, "тест").Collect(context.Background(), &manifest.Manifest{}, &facts.Facts{})

	if got := statusOf(results, "p"); got != StatusOK {
		t.Errorf("заметка принята за изменение: %s", got)
	}
	if len(p.Steps) != 0 {
		t.Errorf("заметка попала в шаги: %+v", p.Steps)
	}
	if len(results[0].Notes) != 1 {
		t.Errorf("заметка потеряна: %+v", results[0].Notes)
	}
}

// Diff считает движок, а не провайдер: провайдер знает, что должно быть
// в файле, но в файловую систему не ходит вовсе.
func TestCollectFillsDiffForWriteSteps(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sources.list")
	if err := os.WriteFile(target, []byte("deb http://enterprise\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := provider.NewRegistry(stub{id: "p", configured: true, changes: plan.Changes{
		Steps: []plan.Step{{ID: "w", Provider: "p", Action: plan.ActionWrite,
			Path: target, Content: []byte("deb http://download\n")}},
	}})

	p, _ := New(reg, nil, "тест").Collect(context.Background(), &manifest.Manifest{}, &facts.Facts{})

	got := p.Steps[0].Diff
	if !strings.Contains(got, "-deb http://enterprise") || !strings.Contains(got, "+deb http://download") {
		t.Errorf("разница посчитана неверно:\n%s", got)
	}
}

func TestCollectMarksMissingFileInDiff(t *testing.T) {
	target := filepath.Join(t.TempDir(), "нет-такого.conf")
	reg := provider.NewRegistry(stub{id: "p", configured: true, changes: plan.Changes{
		Steps: []plan.Step{{ID: "w", Provider: "p", Action: plan.ActionWrite,
			Path: target, Content: []byte("первая строка\n")}},
	}})
	p, _ := New(reg, nil, "тест").Collect(context.Background(), &manifest.Manifest{}, &facts.Facts{})

	if !strings.Contains(p.Steps[0].Diff, "файла нет") {
		t.Errorf("не видно, что файл создаётся с нуля:\n%s", p.Steps[0].Diff)
	}
}

func TestCollectStampsPlanForStalenessCheck(t *testing.T) {
	dir := t.TempDir()
	mPath := filepath.Join(dir, "host.json")
	if err := os.WriteFile(mPath, []byte(`{"storages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Path: mPath}
	f := &facts.Facts{Hostname: "pve-01"}

	p, _ := New(provider.NewRegistry(), nil, "0.3.0").Collect(context.Background(), m, f)

	if p.ManifestS == "" || p.FactsDigest == "" {
		t.Fatal("план собран без отпечатков — устаревание не отловить")
	}
	if stale, why := p.Stale(ManifestDigest(mPath), f.Digest()); stale {
		t.Errorf("свежий план объявлен устаревшим: %s", why)
	}
	if err := os.WriteFile(mPath, []byte(`{"storages":[{"name":"local"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if stale, _ := p.Stale(ManifestDigest(mPath), f.Digest()); !stale {
		t.Error("правка манифеста после сборки плана не замечена")
	}
}

// --- применение ---------------------------------------------------------------

func runner(t *testing.T) (*exec.Runner, *bytes.Buffer) {
	t.Helper()
	log := &bytes.Buffer{}
	return &exec.Runner{Out: &bytes.Buffer{}, Log: log, BackupDir: t.TempDir()}, log
}

func TestApplyRunsEveryStep(t *testing.T) {
	r, _ := runner(t)
	p := &plan.Plan{Steps: []plan.Step{step("a", "true"), step("b", "true")}}

	rep := Apply(context.Background(), r, p)
	if !rep.Ok() || len(rep.Done) != 2 {
		t.Fatalf("не все шаги выполнены: %+v", rep)
	}
}

// Упавший шаг не должен обрывать остальное: независимые изменения человеку
// всё равно нужны, а о сбое он узнает из отчёта.
func TestApplyContinuesAfterFailure(t *testing.T) {
	r, _ := runner(t)
	p := &plan.Plan{Steps: []plan.Step{step("сломается", "false"), step("пройдёт", "true")}}

	rep := Apply(context.Background(), r, p)
	if len(rep.Failed) != 1 || rep.Failed[0].Step.ID != "сломается" {
		t.Fatalf("сбой не зафиксирован: %+v", rep.Failed)
	}
	if len(rep.Done) != 1 || rep.Done[0].ID != "пройдёт" {
		t.Fatalf("следующий шаг не выполнен: %+v", rep.Done)
	}
	if rep.Ok() {
		t.Error("отчёт объявлен успешным, хотя шаг упал")
	}
}

// А вот зависимый шаг выполнять нельзя: заводить хранилище, каталог
// которого не создался, — верный способ получить неработающее хранилище
// и решить, что всё прошло.
func TestApplySkipsStepsWaitingOnFailure(t *testing.T) {
	r, log := runner(t)
	dependent := step("хранилище", "true")
	dependent.Needs = []string{"каталог"}

	p := &plan.Plan{Steps: []plan.Step{step("каталог", "false"), dependent}}
	rep := Apply(context.Background(), r, p)

	if len(rep.Skipped) != 1 || rep.Skipped[0].ID != "хранилище" {
		t.Fatalf("зависимый шаг не пропущен: %+v", rep)
	}
	if !strings.Contains(log.String(), "каталог") {
		t.Errorf("в логе не сказано, из-за чего пропущено:\n%s", log.String())
	}
}

func TestApplyStopsWhenAborted(t *testing.T) {
	r, _ := runner(t)
	r.Confirm = func(s plan.Step, _ string) exec.Decision {
		if s.ID == "второй" {
			return exec.Abort
		}
		return exec.Apply
	}
	p := &plan.Plan{Steps: []plan.Step{step("первый", "true"), step("второй", "true"), step("третий", "true")}}

	rep := Apply(context.Background(), r, p)
	if !rep.Aborted {
		t.Fatal("прерывание не отмечено в отчёте")
	}
	if len(rep.Done) != 1 {
		t.Errorf("после прерывания выполнено лишнее: %+v", rep.Done)
	}
}

func TestVerifyReportsDrift(t *testing.T) {
	reg := provider.NewRegistry(
		stub{id: "в-порядке", configured: true,
			findings: []plan.Finding{{Resource: "local", Message: "в порядке", OK: true}}},
		stub{id: "расходится", configured: true,
			findings: []plan.Finding{{Resource: "local", Message: "нет snippets"}}},
		stub{id: "не-описан", configured: false},
	)
	results := New(reg, nil, "тест").Verify(context.Background(), &manifest.Manifest{}, &facts.Facts{})

	if got := statusOf(results, "в-порядке"); got != StatusOK {
		t.Errorf("исправное состояние: %s", got)
	}
	if got := statusOf(results, "расходится"); got != StatusChanges {
		t.Errorf("расхождение не замечено: %s", got)
	}
	if got := statusOf(results, "не-описан"); got != StatusUnconfigured {
		t.Errorf("правило нуля: %s", got)
	}
}

// Найденное вне манифеста — это не расхождение: keel такое не трогает,
// а только рассказывает.
func TestVerifyDoesNotCountUnmanagedAsDrift(t *testing.T) {
	reg := provider.NewRegistry(stub{id: "p", configured: true, findings: []plan.Finding{
		{Resource: "local", Message: "в порядке", OK: true},
		{Resource: "чужое", Message: "не описано в манифесте", OK: true, Unmanaged: true},
	}})
	results := New(reg, nil, "тест").Verify(context.Background(), &manifest.Manifest{}, &facts.Facts{})
	if results[0].Status != StatusOK {
		t.Errorf("чужое хранилище принято за расхождение: %s", results[0].Status)
	}
}
