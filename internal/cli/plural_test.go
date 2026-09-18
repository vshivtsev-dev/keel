package cli

import "testing"

func TestPlural(t *testing.T) {
	cases := map[int]string{
		0: "0 пакетов", 1: "1 пакет", 2: "2 пакета", 4: "4 пакета", 5: "5 пакетов",
		11: "11 пакетов", 12: "12 пакетов", 14: "14 пакетов", 21: "21 пакет",
		22: "22 пакета", 25: "25 пакетов", 101: "101 пакет", 111: "111 пакетов",
	}
	for n, want := range cases {
		if got := plural(n, "пакет", "пакета", "пакетов"); got != want {
			t.Errorf("plural(%d) = %q, ожидалось %q", n, got, want)
		}
	}
}
