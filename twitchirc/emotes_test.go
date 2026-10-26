package twitchirc

import "testing"

func TestParseEmotesBasic(t *testing.T) {
	emotes := ParseEmotes("25:0-4,11-15/1902:6-8", "Kappa test Kappa")

	if len(emotes) != 3 {
		t.Fatalf("expected 3 emotes, got %+v", emotes)
	}

	if emotes[0].ID != "25" || emotes[0].Name != "Kappa" || emotes[0].Start != 0 || emotes[0].End != 4 {
		t.Fatalf("unexpected first emote: %+v", emotes[0])
	}

	if emotes[1].ID != "1902" || emotes[1].Name != "tes" {
		t.Fatalf("unexpected second emote: %+v", emotes[1])
	}

	if emotes[2].Start != 11 || emotes[2].Name != "Kappa" {
		t.Fatalf("unexpected third emote: %+v", emotes[2])
	}
}

func TestParseEmotesUnicode(t *testing.T) {
	text := "привет Kappa"
	// "привет" = 6 runes, " " = 1 → Kappa = 7-11
	emotes := ParseEmotes("25:7-11", text)

	if len(emotes) != 1 || emotes[0].Name != "Kappa" || emotes[0].Start != 7 {
		t.Fatalf("unexpected unicode emote: %+v", emotes)
	}
}

func TestParseEmotesInvalid(t *testing.T) {
	if got := ParseEmotes("", "text"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}

	if got := ParseEmotes("25:0-99", "short"); len(got) != 0 {
		t.Fatalf("out-of-range must be ignored, got %+v", got)
	}
}
