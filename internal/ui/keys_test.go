package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Без этой пары на русской раскладке не работает вообще ничего, и
// выясняется это в первую же минуту.
func TestCyrillicKeysWorkLikeLatin(t *testing.T) {
	pairs := map[rune]string{
		'a': "a", 'ф': "a", // применить
		'p': "p", 'з': "p", // пересчитать план
		'q': "q", 'й': "q", // выход
		'd': "d", 'в': "d", // подробности
		'k': "k", 'л': "k", // вверх
		'j': "j", 'о': "j", // вниз
		'Ф': "a", 'A': "a", // и в верхнем регистре
	}
	for pressed, want := range pairs {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{pressed}}
		if got := key(msg); got != want {
			t.Errorf("нажатие %q → %q, ожидалось %q", string(pressed), got, want)
		}
	}
}

func TestSpecialKeysPassThrough(t *testing.T) {
	cases := map[tea.KeyType]string{
		tea.KeyUp: "up", tea.KeyDown: "down", tea.KeyEnter: "enter",
		tea.KeyEsc: "esc", tea.KeyTab: "tab", tea.KeySpace: " ",
	}
	for kt, want := range cases {
		if got := key(tea.KeyMsg{Type: kt}); got != want {
			t.Errorf("%v → %q, ожидалось %q", kt, got, want)
		}
	}
}
