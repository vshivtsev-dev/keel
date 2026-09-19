package ui

import tea "github.com/charmbracelet/bubbletea"

// Клавиши ловятся парами: латинская и та, что приходит с русской
// раскладки на той же кнопке.
//
// Это не мелочь. Русский проект, русский интерфейс — раскладка у человека
// с большой вероятностью русская, и «a» приходит как «ф». Без этой пары
// не работает вообще ничего, и выясняется это в первую же минуту.
var latinFor = map[rune]rune{
	'ф': 'a', 'и': 'b', 'с': 'c', 'в': 'd', 'у': 'e', 'а': 'f', 'п': 'g',
	'р': 'h', 'ш': 'i', 'о': 'j', 'л': 'k', 'д': 'l', 'ь': 'm', 'т': 'n',
	'щ': 'o', 'з': 'p', 'й': 'q', 'к': 'r', 'ы': 's', 'е': 't', 'г': 'u',
	'м': 'v', 'ц': 'w', 'ч': 'x', 'н': 'y', 'я': 'z',
	'ю': '.', 'б': ',', 'х': '[', 'ъ': ']', 'ж': ';', 'э': '\'',
}

// key приводит нажатие к латинской букве, чем бы его ни набрали.
func key(msg tea.KeyMsg) string {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return msg.String()
	}
	r := msg.Runes[0]
	if lat, ok := latinFor[toLowerRu(r)]; ok {
		return string(lat)
	}
	return string(toLowerLatin(r))
}

func toLowerRu(r rune) rune {
	if r >= 'А' && r <= 'Я' {
		return r + 32
	}
	if r == 'Ё' {
		return 'ё'
	}
	return r
}

func toLowerLatin(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}
