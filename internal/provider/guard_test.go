package provider_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Правило, на котором держится весь keel: провайдер описывает изменения,
// но не выполняет их. Каждое изменение проходит через ворота — и видно на
// экране до того, как случится.
//
// В bash это стерёг tests/lint-run-guard.sh, искавший прямые вызовы apt-get
// и sed -i в модулях. Здесь проверка строже: разбирается дерево исходника,
// поэтому её не обойти ни переносом строки, ни переменной с именем команды.
//
// Если этот тест упал — не добавляй исключение. Опиши изменение шагом
// плана, и движок выполнит его через ворота.

// Пакеты, которых в провайдере быть не может вовсе.
var forbiddenImports = map[string]string{
	"os/exec":               "провайдер не запускает команды — опиши шаг plan.ActionExec",
	"os/user":               "провайдер не ходит в систему за пользователями — добавь факт в internal/facts",
	"syscall":               "провайдер не трогает систему напрямую",
	"golang.org/x/sys/unix": "провайдер не трогает систему напрямую",
}

// Функции, меняющие файлы. Чтение оставлено: провайдер может прочитать
// конфиг, чтобы посчитать diff, — это не изменение.
var forbiddenCalls = map[string]string{
	"os.WriteFile": "опиши шаг plan.ActionWrite — он покажет diff и снимет копию",
	"os.Create":    "опиши шаг plan.ActionWrite",
	"os.OpenFile":  "опиши шаг plan.ActionWrite",
	"os.Remove":    "удаление описывается шагом плана",
	"os.RemoveAll": "удаление описывается шагом плана",
	"os.Mkdir":     "опиши шаг plan.ActionMkdir",
	"os.MkdirAll":  "опиши шаг plan.ActionMkdir",
	"os.Rename":    "переименование описывается шагом плана",
	"os.Chmod":     "смена прав описывается шагом плана",
	"os.Chown":     "смена владельца описывается шагом плана",
	"os.Symlink":   "ссылка описывается шагом плана",
	"os.Truncate":  "опиши шаг plan.ActionWrite",
}

func providerDir(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("не удалось определить путь к тесту")
	}
	return filepath.Dir(self)
}

func TestProvidersCannotBypassTheGate(t *testing.T) {
	root := providerDir(t)
	fset := token.NewFileSet()

	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		checked++

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("%s: не разбирается: %v", path, err)
			return nil
		}
		rel, _ := filepath.Rel(root, path)

		// Импорты.
		local := map[string]string{} // локальное имя -> путь пакета
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if why, bad := forbiddenImports[p]; bad {
				t.Errorf("%s:%d: запрещённый импорт %q — %s",
					rel, fset.Position(imp.Pos()).Line, p, why)
			}
			name := p[strings.LastIndexByte(p, '/')+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			local[name] = p
		}

		// Вызовы.
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Проверяем только вызовы настоящего os, а не одноимённой
			// переменной: иначе тест ругался бы на чужой код.
			if local[pkg.Name] != "os" {
				return true
			}
			full := pkg.Name + "." + sel.Sel.Name
			if why, bad := forbiddenCalls[full]; bad {
				t.Errorf("%s:%d: %s меняет систему в обход ворот — %s",
					rel, fset.Position(call.Pos()).Line, full, why)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("обход исходников не удался: %v", err)
	}
	if checked == 0 {
		t.Fatal("не проверено ни одного файла — страж смотрит не туда")
	}
}
