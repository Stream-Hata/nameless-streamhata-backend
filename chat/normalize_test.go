package chat

import "testing"

func TestNormalizeText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "translit", in: "privet kak dela", want: "привет как дела"},
		{name: "wrong layout", in: "ghbdtn rfr ltkf", want: "привет как дела"},
		{name: "slang", in: "spasibo bratan", want: "спасибо братан"},
		{name: "digits slang", in: "4to dela", want: "что дела"},
		{name: "digits slang norm", in: "norm", want: "норм"},
		{name: "english untouched", in: "hello how are you", want: ""},
		{name: "emotes untouched", in: "gg wp", want: ""},
		{name: "russian untouched", in: "привет как дела", want: ""},
		{name: "command untouched", in: "!yea", want: ""},
		{name: "homoglyph", in: "привeт", want: "привет"},
		{name: "punctuation", in: "kak dela?", want: "как дела?"},
		{name: "uppercase", in: "PRIVET", want: "ПРИВЕТ"},
		{name: "url", in: "https://example.com", want: ""},
		{name: "ambiguous alone", in: "ya", want: ""},
		{name: "ambiguous in context", in: "ya tebya lyublyu", want: "я тебя люблю"},
		{name: "ambiguous pair", in: "ty kto", want: "ты кто"},
		{name: "single letter", in: "v", want: ""},
		{name: "empty", in: "   ", want: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := normalizeText(testCase.in); got != testCase.want {
				t.Fatalf("normalizeText(%q) = %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestRussianWordsAreCyrillic(t *testing.T) {
	for word := range russianWords {
		for _, r := range word {
			if r >= 'а' && r <= 'я' || r == 'ё' {
				continue
			}

			t.Fatalf("russian word %q contains non-cyrillic rune %q", word, r)
		}
	}
}

func TestEnglishWordsAreLatin(t *testing.T) {
	for word := range englishWords {
		for _, r := range word {
			if r >= 'a' && r <= 'z' {
				continue
			}

			t.Fatalf("english word %q contains non-latin rune %q", word, r)
		}
	}
}
