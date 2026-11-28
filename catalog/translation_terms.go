package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// TranslationTerms returns game titles and platform names that chat translation
// must preserve verbatim, including localized title aliases.
func (s *Service) TranslationTerms() []string {
	seen := make(map[string]struct{})
	terms := make([]string, 0, 4096)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		terms = append(terms, value)
	}

	for _, platform := range PlatformList() {
		add(platform.Name)
		add(platform.Code)
	}
	for _, alias := range localizedPlatformAliases {
		add(alias)
	}

	if s == nil || s.db == nil {
		return terms
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT payload->>'title', payload->>'titleRu', payload->>'originalTitle', payload->'aliases'
		FROM catalog_game`)
	if err != nil {
		return terms
	}
	defer rows.Close()

	for rows.Next() {
		var title, titleRu, originalTitle sql.NullString
		var rawAliases []byte
		if rows.Scan(&title, &titleRu, &originalTitle, &rawAliases) != nil {
			continue
		}
		add(title.String)
		add(titleRu.String)
		add(originalTitle.String)
		var aliases []string
		if json.Unmarshal(rawAliases, &aliases) == nil {
			for _, alias := range aliases {
				add(alias)
			}
		}
	}
	return terms
}

var localizedPlatformAliases = []string{
	"Плейстейшн", "Плейстейшен", "Плейстейшн 2", "Плейстейшен 2", "Плейстейшн 3", "Плейстейшен 3",
	"ПС1", "ПС 1", "ПС2", "ПС 2", "ПС3", "ПС 3", "ПСП",
	"Супер Нинтендо", "Супер Нинтендо Энтертейнмент Систем", "Сега Мега Драйв", "Сега Сатурн",
	"Гейм Бой", "Геймбой", "Гейм Бой Колор", "Геймбой Колор", "Гейм Бой Адванс", "Геймбой Адванс",
	"Нинтендо ДС", "Нинтендо 3ДС", "Нинтендо 64", "Геймкуб", "Дримкаст", "Вии", "Вии Ю",
	"Иксбокс", "Иксбокс 360", "Дос", "Виндоус", "Виндовс", "Стим", "Сега СД", "Атари 2600",
}
