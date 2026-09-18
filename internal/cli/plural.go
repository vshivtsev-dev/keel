package cli

import "github.com/vshivtsev-dev/keel/internal/ru"

// plural — короткая обёртка над общим склонением: в выводе оно нужно на
// каждом шагу, и писать полное имя пакета каждый раз только мешает читать.
func plural(n int, one, few, many string) string { return ru.Plural(n, one, few, many) }
