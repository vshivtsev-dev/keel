package ru

import "testing"

func TestPlural(t *testing.T) {
	cases := map[int]string{
		0: "0 ядер", 1: "1 ядро", 2: "2 ядра", 4: "4 ядра", 5: "5 ядер",
		11: "11 ядер", 12: "12 ядер", 14: "14 ядер", 21: "21 ядро",
		22: "22 ядра", 25: "25 ядер", 101: "101 ядро", 111: "111 ядер",
		1002: "1002 ядра",
	}
	for n, want := range cases {
		if got := Cores(n); got != want {
			t.Errorf("Cores(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}

func TestKnownWords(t *testing.T) {
	cases := []struct{ got, want string }{
		{Step(1), "1 шаг"}, {Step(3), "3 шага"}, {Step(5), "5 шагов"},
		{Package(1), "1 пакет"}, {Guest(2), "2 гостя"}, {Hour(21), "21 час"},
		{Path(2), "2 пути"}, {Change(1), "1 изменение"}, {InAreas(1), "1 области"}, {InAreas(5), "5 областях"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("получено %q, ожидалось %q", c.got, c.want)
		}
	}
}
