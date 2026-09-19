package manifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// Это проверки договора с человеком, а не реализации: манифест правят
// руками, и он обязан принимать то, что в нём написано в документации, —
// чем бы послабления ни снимались внутри.

func TestManifestAcceptsComments(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"две косые", "{\n  // пояснение\n  \"a\": 1\n}", `{"a":1}`},
		{"блочный", "{ /* пояснение */ \"a\": 1 }", `{"a":1}`},
		{"блочный многострочный", "{\n/*\n  два\n  ряда\n*/\n\"a\": 1 }", `{"a":1}`},
		{"висячая запятая в объекте", `{"a": 1,}`, `{"a":1}`},
		{"висячая запятая в массиве", `{"a": [1, 2,]}`, `{"a":[1,2]}`},
		{"висячая через перевод строки", "{\"a\": [1,\n  2,\n]}", `{"a":[1,2]}`},
		{"комментарий перед скобкой", "{\"a\": [1, 2, // хвост\n]}", `{"a":[1,2]}`},
		{"вложенные висячие", `{"a": [[1,],]}`, `{"a":[[1]]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			std, err := Standardize([]byte(c.in))
			if err != nil {
				t.Fatalf("не принято: %v", err)
			}
			var got any
			if err := json.Unmarshal(std, &got); err != nil {
				t.Fatalf("после разбора не читается: %v\n%q", err, std)
			}
			b, _ := json.Marshal(got)
			if string(b) != c.want {
				t.Errorf("получено %s, ожидалось %s", b, c.want)
			}
		})
	}
}

// Самое важное свойство: внутри строки косые, звёздочки и решётка — это
// данные. Ошибка здесь тихо портит пути и пароли.
func TestManifestKeepsStringsIntact(t *testing.T) {
	cases := []string{
		`{"a": "не // комментарий"}`,
		`{"a": "не /* комментарий"}`,
		`{"a": "путь/к/файлу"}`,
		`{"a": "решётка # внутри"}`,
		`{"a": "кавычка \" и следом # решётка"}`,
		`{"a": "обратный слэш в конце \\"}`,
		`{"a": "запятая, и скобка ]"}`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			var want map[string]any
			if err := json.Unmarshal([]byte(in), &want); err != nil {
				t.Fatalf("исходный JSON сам по себе неверен: %v", err)
			}
			std, err := Standardize([]byte(in))
			if err != nil {
				t.Fatalf("не принято: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(std, &got); err != nil {
				t.Fatalf("после разбора не читается: %v\n%q", err, std)
			}
			if want["a"] != got["a"] {
				t.Errorf("значение испорчено: было %q, стало %q", want["a"], got["a"])
			}
		})
	}
}

// Позиция ошибки должна указывать на настоящую строку манифеста, иначе
// опечатку придётся искать глазами.
func TestManifestKeepsLineNumbers(t *testing.T) {
	in := "{\n  // комментарий\n  /* и блочный\n     на два ряда */\n  \"a\": 1\n}"
	got, err := Standardize([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "\n") != strings.Count(in, "\n") {
		t.Errorf("число переводов строки изменилось")
	}
}

// Комментарии «#» работали, пока манифест читался через JSON::PP.
// Отказ формально верен, но человеку нужно знать, что делать.
func TestHashCommentSaysWhatToDo(t *testing.T) {
	_, err := Standardize([]byte("{\n  # старый комментарий\n  \"a\": 1\n}"))
	if err == nil {
		t.Fatal("комментарий «#» принят — документация обещает другое")
	}
	for _, want := range []string{"«#»", "«//»"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q:\n%v", want, err)
		}
	}
}
