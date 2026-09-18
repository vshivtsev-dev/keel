package cli

import "fmt"

// plural склоняет русское существительное по числу: 1 пакет, 2 пакета,
// 5 пакетов. Без этого отчёт говорит «1 пакетов», и сразу видно, что
// его писали наспех.
func plural(n int, one, few, many string) string {
	form := many
	switch mod100 := n % 100; {
	case mod100 >= 11 && mod100 <= 14:
	default:
		switch n % 10 {
		case 1:
			form = one
		case 2, 3, 4:
			form = few
		}
	}
	return fmt.Sprintf("%d %s", n, form)
}
