package catalog

import (
	"testing"
	"time"
)

func TestSplitLine(t *testing.T) {
	title, platform, ok := splitLine("Super Mario Bros. (NES)")
	if !ok || title != "Super Mario Bros." || platform != "NES" {
		t.Fatalf("unexpected split: %q %q %v", title, platform, ok)
	}
}

func TestSplitLineWithParentheses(t *testing.T) {
	title, platform, ok := splitLine("Doom (Shareware) (DOS)")
	if !ok || title != "Doom (Shareware)" || platform != "DOS" {
		t.Fatalf("unexpected split: %q %q %v", title, platform, ok)
	}
}

func TestParseCatalog(t *testing.T) {
	data := []byte("Super Mario Bros. (NES)\nHalf-Life (Windows)\nSonic the Hedgehog (SMD)\n")
	games, bySlug := parseCatalog(data, "test", time.Unix(0, 0))

	if len(games) != 3 {
		t.Fatalf("expected 3 games, got %d", len(games))
	}

	if len(bySlug) != 3 {
		t.Fatalf("expected 3 slugs, got %d", len(bySlug))
	}

	mario := games[0]
	if mario.Platforms[0].Code != "nes" {
		t.Fatalf("expected nes, got %s", mario.Platforms[0].Code)
	}

	if mario.Tier != "platinum" {
		t.Fatalf("expected iconic platinum tier, got %s", mario.Tier)
	}

	if !mario.EmulatorsRequired {
		t.Fatal("NES game must require an emulator")
	}
}

func TestFilterGames(t *testing.T) {
	games := []Game{
		{Slug: "a", Title: "Alpha", Platforms: []Platform{{Code: "nes"}}, Tier: "gold", Age: "classic", Status: "seed"},
		{Slug: "b", Title: "Beta", Platforms: []Platform{{Code: "ps1"}}, Tier: "none", Age: "retro", Status: "seed"},
	}

	result := filterGames(games, Filters{Query: "alpha"})
	if len(result) != 1 || result[0].Slug != "a" {
		t.Fatalf("unexpected query filter: %#v", result)
	}

	result = filterGames(games, Filters{Platform: "ps1"})
	if len(result) != 1 || result[0].Slug != "b" {
		t.Fatalf("unexpected platform filter: %#v", result)
	}

	result = filterGames(games, Filters{Tier: "gold"})
	if len(result) != 1 || result[0].Slug != "a" {
		t.Fatalf("unexpected tier filter: %#v", result)
	}
}

func TestSlugify(t *testing.T) {
	if got := slugify("  Dr. Mario!! "); got != "dr-mario" {
		t.Fatalf("unexpected slug %q", got)
	}
}
