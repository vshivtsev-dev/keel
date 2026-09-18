package exec

import (
	"context"
	"strings"
	"testing"
)

// Команда показывается человеку до того, как выполнится, — значит её надо
// печатать так, чтобы её можно было скопировать в терминал и получить
// ровно то же самое. Наследник проверок cmd_str() из bash-версии.
func TestRenderIsCopyPasteable(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"pvesm", "set", "local", "--content", "iso,backup"}, "pvesm set local --content iso,backup"},
		{[]string{"qm", "create", "100", "--name", "моя вм"}, "qm create 100 --name 'моя вм'"},
		{[]string{"sh", "-c", "echo 'привет'"}, `sh -c 'echo '\''привет'\'''`},
		{[]string{"apt-get", "install", "-y", "pve-headers"}, "apt-get install -y pve-headers"},
		{[]string{"touch", ""}, "touch ''"},
		{[]string{"echo", "a;rm -rf /"}, "echo 'a;rm -rf /'"},
		{[]string{"echo", "$HOME"}, "echo '$HOME'"},
	}
	for _, c := range cases {
		if got := Render(c.argv); got != c.want {
			t.Errorf("Render(%q)\n  получено: %s\n  ожидалось: %s", c.argv, got, c.want)
		}
	}
}

func TestFakeRecordsAndAnswers(t *testing.T) {
	f := NewFake()
	f.Out["pvesm status"] = "local dir active\n"
	f.Missing["lspci"] = true

	out, err := f.Capture(context.Background(), "pvesm", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "local") {
		t.Errorf("ответ подмены: %q", out)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "pvesm status" {
		t.Errorf("вызовы не записаны: %v", f.Calls)
	}
	if f.Has("lspci") {
		t.Error("отсутствующая команда объявлена доступной")
	}
	if !f.Has("pvesm") {
		t.Error("доступная команда объявлена отсутствующей")
	}
}

// Молчаливый пустой ответ на незнакомую команду прятал бы ошибку в тесте:
// провайдер решил бы, что хранилищ нет, и «правильно» ничего не сделал.
func TestFakeComplainsAboutUnknownCommand(t *testing.T) {
	f := NewFake()
	if _, err := f.Capture(context.Background(), "qm", "list"); err == nil {
		t.Fatal("подмена молча ответила на команду, о которой её не предупреждали")
	}
}
