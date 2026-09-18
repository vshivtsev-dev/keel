// Package plan описывает изменения как данные.
//
// Ключевая перемена по сравнению с bash-версией: шаг ничего не делает — он
// рассказывает, что должно произойти. Выполняет только движок, и только
// через ворота. Благодаря этому одно и то же описание показывается на
// экране, сохраняется в файл и исполняется — вместо того чтобы считаться
// заново на каждом из трёх этапов.
package plan

import "fmt"

type Action string

const (
	// ActionExec — выполнить команду.
	ActionExec Action = "exec"
	// ActionWrite — записать файл целиком, показав diff и сняв копию.
	ActionWrite Action = "write"
	// ActionMkdir — создать каталог.
	ActionMkdir Action = "mkdir"
)

// Step — одно изменение системы.
type Step struct {
	// ID устойчив между запусками: по нему возобновляется прерванное
	// применение и сопоставляются шаги двух планов.
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Resource — то, что человек видит строкой в дереве: «хранилище local».
	Resource string `json:"resource"`
	// Summary — одна строка о том, что произойдёт.
	Summary string `json:"summary"`

	Action Action `json:"action"`

	// Для ActionExec.
	Cmd []string `json:"cmd,omitempty"`
	// Interactive отдаёт терминал команде целиком: так apt может спросить
	// про изменённый конфиг и не повиснуть в невидимом окне.
	Interactive bool `json:"interactive,omitempty"`

	// Для ActionWrite и ActionMkdir.
	Path string `json:"path,omitempty"`
	// Content — то, что будет записано. Diff считается при сборке плана,
	// чтобы показать его до применения и не читать файл дважды.
	Content []byte `json:"content,omitempty"`
	Diff    string `json:"diff,omitempty"`

	// Needs — идентификаторы шагов, которые должны пройти раньше.
	Needs []string `json:"needs,omitempty"`
	// Unknown перечисляет то, что станет известно только при применении:
	// точный список пакетов apt, имя файла свежего образа. Показывается
	// человеку явно, чтобы план не обещал больше, чем знает.
	Unknown []string `json:"unknown,omitempty"`
	// SecretRefs — имена файлов в secrets/. Значений секретов в плане нет
	// и быть не может: план лежит на диске обычным файлом.
	SecretRefs []string `json:"secret_refs,omitempty"`
}

func (s Step) String() string {
	return fmt.Sprintf("%s: %s", s.ID, s.Summary)
}

// Finding — расхождение, найденное проверкой. В отличие от Step ничего
// не предлагает выполнить: это отчёт, а не действие.
type Finding struct {
	Provider string `json:"provider"`
	Resource string `json:"resource"`
	Message  string `json:"message"`
	// OK — расхождения нет, состояние такое, как описано.
	OK bool `json:"ok"`
	// Unmanaged — есть на хосте, но в манифесте не описано. keel такое
	// не трогает никогда, только рассказывает.
	Unmanaged bool `json:"unmanaged,omitempty"`
}
