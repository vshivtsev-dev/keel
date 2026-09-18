// Package engine собирает план из провайдеров и исполняет его.
//
// Разделение простое: провайдеры знают, что должно быть; движок знает,
// когда об этом спрашивать и как это выполнить. Всё, что меняет систему,
// проходит здесь через ворота exec.Runner — и только здесь.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/vshivtsev-dev/keel/internal/diff"
	"github.com/vshivtsev-dev/keel/internal/exec"
	"github.com/vshivtsev-dev/keel/internal/facts"
	"github.com/vshivtsev-dev/keel/internal/manifest"
	"github.com/vshivtsev-dev/keel/internal/plan"
	"github.com/vshivtsev-dev/keel/internal/provider"
)

// Status — во что превратился провайдер при сборке плана.
type Status string

const (
	// StatusOK — состояние уже такое, как описано.
	StatusOK Status = "ok"
	// StatusChanges — есть что менять.
	StatusChanges Status = "changes"
	// StatusUnconfigured — правило нуля: в манифесте про эту область
	// ничего не сказано, значит keel не делает ничего. Это норма.
	StatusUnconfigured Status = "unconfigured"
	// StatusFailed — провайдер не смог разобраться в состоянии хоста.
	StatusFailed Status = "failed"
)

// Result — что вышло у одного провайдера.
type Result struct {
	Provider string
	Title    string
	Status   Status
	Steps    []plan.Step
	Notes    []plan.Note
	Findings []plan.Finding
	Err      error
}

// Engine связывает набор провайдеров с окружением, в котором они работают.
type Engine struct {
	Registry *provider.Registry
	// Sys отображает системные пути. Нужен, чтобы посчитать разницу по
	// тому файлу, который и правда будет записан: при заданном
	// KEEL_FS_ROOT это файл во временном каталоге, а не на живой машине.
	Sys     func(string) string
	Version string
}

func New(reg *provider.Registry, sys func(string) string, version string) *Engine {
	return &Engine{Registry: reg, Sys: sys, Version: version}
}

func (e *Engine) sys(path string) string {
	if e.Sys == nil {
		return path
	}
	return e.Sys(path)
}

// Collect строит план. Ничего не выполняет и не трогает.
func (e *Engine) Collect(
	ctx context.Context,
	m *manifest.Manifest,
	f *facts.Facts,
) (*plan.Plan, []Result) {
	reg, keelVersion := e.Registry, e.Version
	results := make([]Result, 0, len(reg.All()))
	p := &plan.Plan{
		Version:     plan.FormatVersion,
		Created:     time.Now(),
		Host:        f.Hostname,
		KeelVer:     keelVersion,
		Manifest:    m.Path,
		ManifestS:   manifestDigest(m.Path),
		FactsDigest: f.Digest(),
	}

	for _, pr := range reg.All() {
		r := Result{Provider: pr.ID(), Title: pr.Title()}

		if !pr.Configured(m) {
			r.Status = StatusUnconfigured
			results = append(results, r)
			continue
		}

		changes, err := pr.Plan(ctx, m, f)
		r.Notes = changes.Notes
		switch {
		case err != nil:
			r.Status, r.Err = StatusFailed, err
		case changes.Empty():
			r.Status = StatusOK
		case len(changes.Steps) == 0:
			// Менять нечего, но есть о чём рассказать: keel видит расхождение,
			// а исправить его сам не может.
			r.Status = StatusOK
		default:
			r.Status = StatusChanges
			r.Steps = e.fillDiffs(changes.Steps)
			p.Steps = append(p.Steps, r.Steps...)
		}
		results = append(results, r)
	}
	return p, results
}

// fillDiffs дочитывает нынешнее содержимое правленых файлов и считает
// разницу. Делает это движок, а не провайдер: провайдер знает, что должно
// быть в файле, но в файловую систему не ходит вовсе.
func (e *Engine) fillDiffs(steps []plan.Step) []plan.Step {
	out := make([]plan.Step, len(steps))
	copy(out, steps)
	for i := range out {
		if out[i].Action != plan.ActionWrite || out[i].Diff != "" {
			continue
		}
		current, err := os.ReadFile(e.sys(out[i].Path))
		label := out[i].Path + " (сейчас)"
		if err != nil {
			current, label = nil, out[i].Path+" (файла нет)"
		}
		out[i].Diff = diff.Unified(string(current), withNewline(string(out[i].Content)),
			label, out[i].Path+" (станет)")
	}
	return out
}

func withNewline(s string) string {
	if s == "" || s[len(s)-1] == '\n' {
		return s
	}
	return s + "\n"
}

// Verify спрашивает провайдеров, соответствует ли хост описанию.
func (e *Engine) Verify(
	ctx context.Context,
	m *manifest.Manifest,
	f *facts.Facts,
) []Result {
	reg := e.Registry
	results := make([]Result, 0, len(reg.All()))
	for _, pr := range reg.All() {
		r := Result{Provider: pr.ID(), Title: pr.Title()}
		if !pr.Configured(m) {
			r.Status = StatusUnconfigured
			results = append(results, r)
			continue
		}
		findings, err := pr.Verify(ctx, m, f)
		switch {
		case err != nil:
			r.Status, r.Err = StatusFailed, err
		default:
			r.Status = StatusOK
			for _, fi := range findings {
				if !fi.OK && !fi.Unmanaged {
					r.Status = StatusChanges
					break
				}
			}
		}
		r.Findings = findings
		results = append(results, r)
	}
	return results
}

// ApplyReport — что случилось при применении плана.
type ApplyReport struct {
	Done    []plan.Step
	Skipped []plan.Step
	// Guarded — шаги, которые keel отказался начинать: условие не
	// выполнилось. Это не сбой, и в ошибку применения они не идут.
	Guarded []StepError
	Failed  []StepError
	Aborted bool
}

type StepError struct {
	Step plan.Step
	Err  error
}

func (r ApplyReport) Ok() bool { return len(r.Failed) == 0 && !r.Aborted }

// Apply исполняет готовый план шаг за шагом.
//
// Упавший шаг не обрывает применение: остальные независимые изменения
// человеку всё равно нужны, а о сбое он узнает из отчёта. Обрывает только
// его собственное решение прервать — и шаги, которые ждали упавшего.
func Apply(ctx context.Context, r *exec.Runner, p *plan.Plan) ApplyReport {
	var rep ApplyReport
	failed := map[string]bool{}

	for _, step := range p.Steps {
		if blocker, ok := blockedBy(step, failed); ok {
			rep.Skipped = append(rep.Skipped, step)
			failed[step.ID] = true
			r.Log.Write([]byte(fmt.Sprintf("ПРОПУЩЕНО: %s — не выполнен шаг %s\n", step.ID, blocker)))
			continue
		}

		err := r.Do(ctx, step)
		var guarded *exec.ErrGuarded
		switch {
		case err == nil:
			rep.Done = append(rep.Done, step)
		case errors.Is(err, exec.ErrAborted):
			rep.Aborted = true
			return rep
		case errors.As(err, &guarded):
			// Условие не выполнилось — шаг не начат. Зависимые шаги тоже
			// выполнять нельзя: ставить обновления, список которых не
			// обновился, — не то же самое, что не ставить их вовсе.
			rep.Guarded = append(rep.Guarded, StepError{Step: step, Err: err})
			failed[step.ID] = true
		default:
			rep.Failed = append(rep.Failed, StepError{Step: step, Err: err})
			failed[step.ID] = true
		}
	}
	return rep
}

func blockedBy(step plan.Step, failed map[string]bool) (string, bool) {
	for _, need := range step.Needs {
		if failed[need] {
			return need, true
		}
	}
	return "", false
}

func manifestDigest(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ManifestDigest считает отпечаток манифеста — по нему видно, правили ли
// его после сборки плана.
func ManifestDigest(path string) string { return manifestDigest(path) }

// SortedIDs — идентификаторы шагов плана по порядку. Для сообщений.
func SortedIDs(p *plan.Plan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}
