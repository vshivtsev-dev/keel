// Package diff показывает, чем новое содержимое файла отличается от
// нынешнего.
//
// Это не украшение: правка конфига — самое опасное, что делает keel, и
// человек должен увидеть её целиком до того, как она случится. В bash эту
// работу делал diff -u, здесь она своя — тянуть ради неё зависимость в
// инструмент восстановления незачем.
package diff

import (
	"fmt"
	"strings"
)

// Unified строит разницу в привычном формате diff -u.
// Пустая строка означает, что содержимое совпадает.
func Unified(oldText, newText, oldLabel, newLabel string) string {
	if oldText == newText {
		return ""
	}
	a, b := splitLines(oldText), splitLines(newText)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldLabel, newLabel)
	for _, h := range hunks(a, b, 3) {
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", h.aStart+1, h.aLen, h.bStart+1, h.bLen)
		for _, l := range h.lines {
			out.WriteString(l)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

type op struct {
	kind byte // ' ' совпало, '-' убрано, '+' добавлено
	text string
}

type hunk struct {
	aStart, aLen int
	bStart, bLen int
	lines        []string
}

// script строит последовательность правок через наибольшую общую
// подпоследовательность. Файлы конфигурации короткие, поэтому таблица
// на O(n·m) здесь уместнее хитрого алгоритма.
func script(a, b []string) []op {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}

	var out []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, op{' ', a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, op{'-', a[i]})
			i++
		default:
			out = append(out, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, op{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, op{'+', b[j]})
	}
	return out
}

// hunks режет правки на куски, оставляя вокруг каждой по context строк:
// так на экран попадает изменение, а не весь файл.
func hunks(a, b []string, context int) []hunk {
	ops := script(a, b)

	// Какие позиции в списке правок стоит показать.
	show := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for j := max(0, i-context); j <= min(len(ops)-1, i+context); j++ {
			show[j] = true
		}
	}

	var out []hunk
	ai, bi := 0, 0
	for i := 0; i < len(ops); {
		if !show[i] {
			switch ops[i].kind {
			case ' ':
				ai, bi = ai+1, bi+1
			case '-':
				ai++
			case '+':
				bi++
			}
			i++
			continue
		}
		h := hunk{aStart: ai, bStart: bi}
		for ; i < len(ops) && show[i]; i++ {
			o := ops[i]
			h.lines = append(h.lines, string(o.kind)+o.text)
			switch o.kind {
			case ' ':
				ai, bi, h.aLen, h.bLen = ai+1, bi+1, h.aLen+1, h.bLen+1
			case '-':
				ai, h.aLen = ai+1, h.aLen+1
			case '+':
				bi, h.bLen = bi+1, h.bLen+1
			}
		}
		out = append(out, h)
	}
	return out
}
