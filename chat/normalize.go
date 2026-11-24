package chat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// normalizeText исправляет сообщения, написанные транслитом ("privet kak dela")
// или в неправильной раскладке ("ghbdtn rfr ltkf"), а также слова со
// смешанными алфавитами. Обычный русский и английский текст не трогает.
// Возвращает пустую строку, если менять нечего.
func normalizeText(text string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	var (
		changes   = make(map[int]string)
		latin     int
		strong    int
		medium    int
		ambiguous []int
	)

	for index, raw := range words {
		if converted, ok := slangTranslit[strings.ToLower(trimmedToken(raw))]; ok {
			_, _, suffix := splitToken(raw)

			changes[index] = matchCase(raw, converted) + suffix
			latin++
			strong++

			continue
		}

		prefix, core, suffix := splitToken(raw)
		if core == "" {
			continue
		}

		lower := strings.ToLower(core)

		if hasCyrillic(lower) && hasLatin(lower) {
			latin++

			if converted, ok := fixHomoglyphs(lower); ok {
				changes[index] = prefix + matchCase(core, converted) + suffix
				strong++
			}

			continue
		}

		if !hasLatin(lower) {
			continue
		}

		if !isLatinWord(lower) {
			continue
		}

		latin++

		if _, ok := englishWords[lower]; ok {
			continue
		}

		if _, ok := ambiguousLatin[lower]; ok {
			ambiguous = append(ambiguous, index)

			continue
		}

		if converted, ok := translitWords[lower]; ok {
			changes[index] = prefix + matchCase(core, converted) + suffix
			strong++

			continue
		}

		if converted := layout(lower); isRussianWord(converted) {
			changes[index] = prefix + matchCase(core, converted) + suffix
			strong++

			continue
		}

		if converted := translitToCyrillic(lower); converted != lower {
			if _, ok := russianWords[converted]; ok {
				changes[index] = prefix + matchCase(core, converted) + suffix
				medium++
			}
		}
	}

	applyAmbiguous := strong > 0 || len(ambiguous) >= 2
	if applyAmbiguous {
		for _, index := range ambiguous {
			prefix, core, suffix := splitToken(words[index])
			lower := strings.ToLower(core)

			converted, ok := translitWords[lower]
			if !ok {
				converted = layout(lower)
				ok = isRussianWord(converted)
			}

			if !ok {
				continue
			}

			changes[index] = prefix + matchCase(core, converted) + suffix
		}
	}

	if (strong == 0 && !applyAmbiguous && medium < 2) || (latin == 0) {
		return ""
	}

	result := make([]string, len(words))
	copied := false

	for index, word := range words {
		if replacement, ok := changes[index]; ok {
			result[index] = replacement
			copied = true
		} else {
			result[index] = word
		}
	}

	if !copied {
		return ""
	}

	return strings.Join(result, " ")
}

func trimmedToken(raw string) string {
	return strings.Trim(raw, "!?.,;:()[]{}\"'«»~`")
}

// splitToken отделяет ведущую и хвостовую пунктуацию от слова.
func splitToken(raw string) (string, string, string) {
	runes := []rune(raw)
	start := 0

	for start < len(runes) && !unicode.IsLetter(runes[start]) {
		start++
	}

	end := len(runes)

	for end > start && !unicode.IsLetter(runes[end-1]) {
		end--
	}

	if start == end {
		return raw, "", ""
	}

	return string(runes[:start]), string(runes[start:end]), string(runes[end:])
}

func hasLatin(text string) bool {
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return true
		}
	}

	return false
}

func hasCyrillic(text string) bool {
	for _, r := range text {
		if r >= 'а' && r <= 'я' || r == 'ё' || r == 'Ё' || r >= 'А' && r <= 'Я' {
			return true
		}
	}

	return false
}

func isLatinWord(word string) bool {
	if len(word) < 2 {
		return false
	}

	for _, r := range word {
		if r < 'a' || r > 'z' {
			return false
		}
	}

	return true
}

func matchCase(original, converted string) string {
	if original != strings.ToLower(original) && original == strings.ToUpper(original) {
		return strings.ToUpper(converted)
	}

	first, _ := utf8.DecodeRuneInString(original)
	if unicode.IsUpper(first) {
		runes := []rune(converted)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}

		return string(runes)
	}

	return converted
}

var qwertyToRus = map[rune]rune{
	'q': 'й', 'w': 'ц', 'e': 'у', 'r': 'к', 't': 'е', 'y': 'н', 'u': 'г',
	'i': 'ш', 'o': 'щ', 'p': 'з', '[': 'х', ']': 'ъ',
	'a': 'ф', 's': 'ы', 'd': 'в', 'f': 'а', 'g': 'п', 'h': 'р', 'j': 'о',
	'k': 'л', 'l': 'д', ';': 'ж', '\'': 'э',
	'z': 'я', 'x': 'ч', 'c': 'с', 'v': 'м', 'b': 'и', 'n': 'т', 'm': 'ь',
	',': 'б', '.': 'ю', '/': '.',
}

func layout(word string) string {
	var builder strings.Builder

	for _, r := range word {
		if mapped, ok := qwertyToRus[r]; ok {
			builder.WriteRune(mapped)
		} else {
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

var homoglyphs = map[rune]rune{
	'a': 'а', 'b': 'в', 'c': 'с', 'e': 'е', 'h': 'н', 'k': 'к', 'm': 'м',
	'o': 'о', 'p': 'р', 't': 'т', 'u': 'и', 'x': 'х', 'y': 'у',
}

func isRussianWord(word string) bool {
	_, ok := russianWords[word]

	return ok
}

func fixHomoglyphs(word string) (string, bool) {
	var builder strings.Builder

	for _, r := range word {
		if mapped, ok := homoglyphs[r]; ok {
			builder.WriteRune(mapped)
		} else {
			builder.WriteRune(r)
		}
	}

	result := builder.String()
	if !isRussianWord(result) {
		return "", false
	}

	return result, true
}

var ruToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "",
	'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

var translitReplacers = []struct{ from, to string }{
	{"h", "kh"},
	{"sch", "shch"},
	{"sch", "sh"},
	{"ts", "c"},
	{"ya", "ia"},
	{"yu", "iu"},
	{"zh", "j"},
	{"y", "i"},
}

func russianToLatin(word string) string {
	var builder strings.Builder

	for _, r := range word {
		if mapped, ok := ruToLatin[r]; ok {
			builder.WriteString(mapped)
		} else {
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

func buildTranslitWords() map[string]string {
	table := make(map[string]string, len(russianWords)*9)

	for word := range russianWords {
		primary := russianToLatin(word)
		if primary == "" || primary == word {
			continue
		}

		if _, exists := table[primary]; !exists {
			table[primary] = word
		}

		for _, replacer := range translitReplacers {
			variant := strings.ReplaceAll(primary, replacer.from, replacer.to)
			if variant == primary {
				continue
			}

			if _, exists := table[variant]; !exists {
				table[variant] = word
			}
		}
	}

	for latin, cyrillic := range slangTranslit {
		table[latin] = cyrillic
	}

	return table
}

var multiLetterTranslit = []struct{ from, to string }{
	{"shch", "щ"}, {"sch", "щ"}, {"zh", "ж"}, {"kh", "х"}, {"ts", "ц"},
	{"ch", "ч"}, {"sh", "ш"}, {"yo", "ё"}, {"ya", "я"}, {"yu", "ю"},
	{"ye", "е"}, {"ay", "ай"}, {"ey", "ей"}, {"oy", "ой"}, {"uy", "уй"},
}

var singleLetterTranslit = map[rune]string{
	'a': "а", 'b': "б", 'c': "к", 'd': "д", 'e': "е", 'f': "ф", 'g': "г",
	'h': "х", 'i': "и", 'j': "дж", 'k': "к", 'l': "л", 'm': "м", 'n': "н",
	'o': "о", 'p': "п", 'q': "к", 'r': "р", 's': "с", 't': "т", 'u': "у",
	'v': "в", 'w': "в", 'x': "кс", 'y': "ы", 'z': "з", '4': "ч",
}

func translitToCyrillic(word string) string {
	var builder strings.Builder

	for index := 0; index < len(word); {
		matched := false

		for _, pair := range multiLetterTranslit {
			if strings.HasPrefix(word[index:], pair.from) {
				builder.WriteString(pair.to)
				index += len(pair.from)
				matched = true

				break
			}
		}

		if matched {
			continue
		}

		if mapped, ok := singleLetterTranslit[rune(word[index])]; ok {
			builder.WriteString(mapped)
		} else {
			builder.WriteByte(word[index])
		}

		index++
	}

	return builder.String()
}
