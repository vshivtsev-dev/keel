package manifest

// Манифест — это JSON с послаблениями: комментарии «#» и «//» до конца
// строки, блочные «/* */» и висячие запятые. Ровно то, что давал режим
// relaxed у JSON::PP, на котором keel читал манифест раньше.
//
// Разбирать такой текст сам encoding/json не умеет, поэтому послабления
// снимаются здесь, до него. Внешней библиотеки для этого нет намеренно:
// единственная сложность — не трогать содержимое строк, а ради неё тянуть
// зависимость в инструмент восстановления не стоит.

// Relax убирает из JSON комментарии и висячие запятые.
//
// Длина текста и нумерация строк сохраняются: комментарии заменяются
// пробелами, переводы строк остаются на месте. Благодаря этому позиция
// ошибки, о которой скажет encoding/json, указывает на настоящую строку
// манифеста, а не на строку в обрезанном тексте.
func Relax(src []byte) []byte {
	return dropTrailingCommas(dropComments(src))
}

func dropComments(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	const (
		code = iota
		str
		line  // # ... и // ... до конца строки
		block // /* ... */
	)

	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '"':
				state = str
			case c == '#':
				state = line
				out[i] = ' '
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = line
				out[i], out[i+1] = ' ', ' '
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = block
				out[i], out[i+1] = ' ', ' '
				i++
			}
		case str:
			// Экранированный символ проглатывается целиком: иначе \" был бы
			// принят за конец строки, а всё дальше — за код.
			if c == '\\' && i+1 < len(src) {
				i++
				continue
			}
			if c == '"' {
				state = code
			}
		case line:
			if c == '\n' {
				state = code
				continue // перевод строки остаётся, чтобы не съехала нумерация
			}
			out[i] = ' '
		case block:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
				continue
			}
			if c != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}

func dropTrailingCommas(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	inStr := false
	lastComma := -1
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			if c == '\\' && i+1 < len(src) {
				i++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			// пробелы между запятой и скобкой запятую не отменяют
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' '
			}
			lastComma = -1
		case c == ',':
			lastComma = i
		default:
			lastComma = -1
			if c == '"' {
				inStr = true
			}
		}
	}
	return out
}
