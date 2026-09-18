package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRelaxDropsComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // ожидаемый JSON после разбора, в канонической форме
	}{
		{"решётка", "{\n  # так было в JSON::PP relaxed\n  \"a\": 1\n}", `{"a":1}`},
		{"две косые", "{\n  // как в JSONC\n  \"a\": 1\n}", `{"a":1}`},
		{"блочный", "{ /* пояснение */ \"a\": 1 }", `{"a":1}`},
		{"блочный многострочный", "{\n/*\n  два\n  ряда\n*/\n\"a\": 1 }", `{"a":1}`},
		{"висячая запятая в объекте", `{"a": 1,}`, `{"a":1}`},
		{"висячая запятая в массиве", `{"a": [1, 2,]}`, `{"a":[1,2]}`},
		{"висячая запятая через перевод строки", "{\"a\": [1,\n  2,\n]}", `{"a":[1,2]}`},
		{"комментарий перед скобкой", "{\"a\": [1, 2, // хвост\n]}", `{"a":[1,2]}`},
		{"вложенные висячие", `{"a": [[1,],]}`, `{"a":[[1]]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got any
			if err := json.Unmarshal(Relax([]byte(c.in)), &got); err != nil {
				t.Fatalf("разбор не удался: %v\nпосле Relax: %q", err, Relax([]byte(c.in)))
			}
			b, _ := json.Marshal(got)
			if string(b) != c.want {
				t.Errorf("получено %s, ожидалось %s", b, c.want)
			}
		})
	}
}

// Самое важное свойство: внутри строки решётка, косые и звёздочки — это
// данные, а не комментарий. Ошибка здесь тихо портит пути и пароли.
func TestRelaxKeepsStrings(t *testing.T) {
	cases := []string{
		`{"a": "не # комментарий"}`,
		`{"a": "не // комментарий"}`,
		`{"a": "не /* комментарий"}`,
		`{"a": "путь/к/файлу"}`,
		`{"a": "кавычка \" и следом # решётка"}`,
		`{"a": "обратный слэш в конце \\"}`,
		`{"a": "запятая, и скобка ]"}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			var want, got map[string]any
			if err := json.Unmarshal([]byte(in), &want); err != nil {
				t.Fatalf("исходный JSON сам по себе неверен: %v", err)
			}
			if err := json.Unmarshal(Relax([]byte(in)), &got); err != nil {
				t.Fatalf("после Relax не разбирается: %v\n%q", err, Relax([]byte(in)))
			}
			if want["a"] != got["a"] {
				t.Errorf("значение испорчено: было %q, стало %q", want["a"], got["a"])
			}
		})
	}
}

// Relax не должен сдвигать строки: иначе сообщение об ошибке от encoding/json
// укажет не на ту строку манифеста, и искать опечатку придётся глазами.
func TestRelaxKeepsLineNumbers(t *testing.T) {
	in := "{\n  # комментарий\n  /* и блочный\n     на два ряда */\n  \"a\": 1\n}"
	got := Relax([]byte(in))
	if len(got) != len(in) {
		t.Fatalf("длина изменилась: было %d, стало %d", len(in), len(got))
	}
	if strings.Count(string(got), "\n") != strings.Count(in, "\n") {
		t.Errorf("число переводов строки изменилось")
	}
}
