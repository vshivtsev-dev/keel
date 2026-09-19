// Package ru — мелочи русского языка, из-за которых вывод выглядит
// написанным наспех.
//
// «2 ядер» и «1 пакетов» читаются как ошибка в подсчёте, а не как
// небрежность в тексте, — и человек начинает искать несуществующую
// проблему вместо того, чтобы читать план.
package ru

import "strconv"

// Plural склоняет существительное по числу: 1 ядро, 2 ядра, 5 ядер.
func Plural(n int, one, few, many string) string {
	form := many
	if mod100 := n % 100; mod100 < 11 || mod100 > 14 {
		switch n % 10 {
		case 1:
			form = one
		case 2, 3, 4:
			form = few
		}
	}
	return strconv.Itoa(n) + " " + form
}

// Известные слова — чтобы одни и те же вещи склонялись одинаково во всём
// выводе, а не по-разному в каждом месте.
func Cores(n int) string   { return Plural(n, "ядро", "ядра", "ядер") }
func Step(n int) string    { return Plural(n, "шаг", "шага", "шагов") }
func Package(n int) string { return Plural(n, "пакет", "пакета", "пакетов") }
func Guest(n int) string   { return Plural(n, "гость", "гостя", "гостей") }
func Hour(n int) string    { return Plural(n, "час", "часа", "часов") }
func Minute(n int) string  { return Plural(n, "минуту", "минуты", "минут") }
func Path(n int) string    { return Plural(n, "путь", "пути", "путей") }
func Change(n int) string {
	return Plural(n, "изменение", "изменения", "изменений")
}

// InAreas — предложный падеж: «в 1 области», «в 2 областях». Отдельно от
// именительного нарочно: «2 шага в 2 области» читается как ошибка.
func InAreas(n int) string {
	return Plural(n, "области", "областях", "областях")
}
