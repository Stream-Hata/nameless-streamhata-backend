package catalog

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const translationBatchSize = 20

// SaveDescriptionRu persists a translated description in the catalog payload.
func (s *Service) SaveDescriptionRu(slug, translated string) {
	if s.db == nil || strings.TrimSpace(slug) == "" || strings.TrimSpace(translated) == "" {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`UPDATE catalog_game
		 SET payload = jsonb_set(payload, '{descriptionRu}', to_jsonb($2::text), true),
		     updated_at = now()
		 WHERE slug = $1
		   AND COALESCE(payload->>'descriptionRu', '') = ''`,
		slug,
		translated,
	)
}

// StartTranslationWorker fills missing Russian descriptions in the background.
// It is intentionally conservative: small batches and a delay between them.
func (s *Service) StartTranslationWorker(ctx context.Context) {
	if s.db == nil || strings.TrimSpace(s.sourceURL) == "" {
		return
	}

	go func() {
		seen := make(map[string]bool)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			rows, err := s.db.QueryContext(ctx, `
				SELECT slug, COALESCE(payload->>'description', '')
				FROM catalog_game
				WHERE COALESCE(payload->>'description', '') <> ''
				  AND COALESCE(payload->>'descriptionRu', '') = ''
				ORDER BY updated_at ASC
				LIMIT $1`, translationBatchSize)
			if err != nil {
				continue
			}

			type item struct{ slug, text string }
			items := make([]item, 0, translationBatchSize)
			for rows.Next() {
				var entry item
				if rows.Scan(&entry.slug, &entry.text) == nil && !seen[entry.slug] {
					items = append(items, entry)
				}
			}
			_ = rows.Close()

			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)
			for _, entry := range items {
				entry := entry
				seen[entry.slug] = true
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					if containsCyrillic(entry.text) {
						s.SaveDescriptionRu(entry.slug, entry.text)
						return
					}
					translated := s.enricher.translateRu(ctx, entry.text)
					if translated != "" {
						s.SaveDescriptionRu(entry.slug, translated)
					}
				}()
			}
			wg.Wait()

			if len(items) > 0 {
				log.Printf("[translate-worker] processed %d descriptions", len(items))
			}
		}
	}()
}
