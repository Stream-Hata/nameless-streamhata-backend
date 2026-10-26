package catalog

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type SearchAlias struct {
	Term        string    `json:"term"`
	Replacement string    `json:"replacement"`
	GameSlug    string    `json:"gameSlug,omitempty"`
	Locale      string    `json:"locale"`
	Weight      int       `json:"weight"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (s *Service) SearchAliases(gameSlug string) ([]SearchAlias, error) {
	if s.db == nil {
		return []SearchAlias{}, nil
	}

	query := `SELECT term, COALESCE(replacement, ''), COALESCE(game_slug, ''), locale, weight, updated_at
		FROM search_synonyms`
	args := make([]any, 0, 1)

	if strings.TrimSpace(gameSlug) == "" {
		query += ` WHERE game_slug IS NULL`
	} else {
		query += ` WHERE game_slug = $1`
		args = append(args, strings.TrimSpace(gameSlug))
	}

	query += ` ORDER BY normalized_term`

	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rows.Close()
	}()

	result := make([]SearchAlias, 0, 64)

	for rows.Next() {
		var item SearchAlias

		if rows.Scan(&item.Term, &item.Replacement, &item.GameSlug, &item.Locale, &item.Weight, &item.UpdatedAt) == nil {
			result = append(result, item)
		}
	}

	return result, nil
}

func (s *Service) SaveSearchAlias(term string, replacement string, gameSlug string) error {
	term = normalizeSearchQuery(term)
	replacement = normalizeSearchQuery(replacement)
	gameSlug = strings.TrimSpace(gameSlug)

	if term == "" || s.db == nil {
		return sql.ErrNoRows
	}

	if gameSlug == "" && replacement == "" {
		return sql.ErrNoRows
	}

	if gameSlug != "" {
		var exists bool

		if err := s.db.QueryRowContext(
			context.Background(),
			`SELECT EXISTS(SELECT 1 FROM catalog_game WHERE slug = $1)`,
			gameSlug,
		).Scan(&exists); err != nil || !exists {
			return sql.ErrNoRows
		}
	}

	_, err := s.db.ExecContext(
		context.Background(),
		`INSERT INTO search_synonyms (game_slug, term, replacement, normalized_term, normalized_replacement)
		 VALUES (NULLIF($1, ''), $2, NULLIF($3, ''), $4, NULLIF($5, ''))
		 ON CONFLICT (game_slug, normalized_term) DO UPDATE SET
		   term = EXCLUDED.term,
		   replacement = EXCLUDED.replacement,
		   normalized_replacement = EXCLUDED.normalized_replacement,
		   updated_at = now()`,
		gameSlug,
		strings.TrimSpace(term),
		strings.TrimSpace(replacement),
		term,
		replacement,
	)

	return err
}

func (s *Service) DeleteSearchAlias(term string, gameSlug string) error {
	term = normalizeSearchQuery(term)
	gameSlug = strings.TrimSpace(gameSlug)

	if term == "" || s.db == nil {
		return sql.ErrNoRows
	}

	_, err := s.db.ExecContext(
		context.Background(),
		`DELETE FROM search_synonyms
		 WHERE normalized_term = $1 AND COALESCE(game_slug, '') = COALESCE($2, '')`,
		term,
		gameSlug,
	)

	return err
}

func (s *Service) gameSynonymSlugs(query string) []string {
	if s.db == nil {
		return nil
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT DISTINCT game_slug FROM search_synonyms
		 WHERE enabled AND game_slug IS NOT NULL AND (
		   normalized_term LIKE '%' || $1 || '%' OR
		   word_similarity(normalized_term, $1) > 0.5
		 )
		 LIMIT 200`,
		query,
	)
	if err != nil {
		return nil
	}

	defer func() {
		_ = rows.Close()
	}()

	result := make([]string, 0, 16)

	for rows.Next() {
		var slug string

		if rows.Scan(&slug) == nil {
			result = append(result, slug)
		}
	}

	return result
}

type AdminUser struct {
	ID          int        `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"displayName"`
	TwitchID    int64      `json:"twitchId"`
	Avatar      string     `json:"avatar,omitempty"`
	HasToken    bool       `json:"hasToken"`
	Banned      bool       `json:"banned"`
	BanReason   string     `json:"banReason"`
	BannedAt    *time.Time `json:"bannedAt,omitempty"`
	UpdatedAt   string     `json:"updatedAt"`
}

func (s *Service) AdminUsers() ([]AdminUser, error) {
	if s.db == nil {
		return []AdminUser{}, nil
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT u.id, u.username, u.display_name, u.twitch_user_id, u.avatar,
		        (t.id IS NOT NULL), u.banned, u.ban_reason, u.banned_at, u.updated_at
		 FROM users u LEFT JOIN tokens t ON t.user_id = u.id
		 ORDER BY u.id`,
	)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rows.Close()
	}()

	result := make([]AdminUser, 0, 16)

	for rows.Next() {
		var item AdminUser
		var bannedAt sql.NullTime

		if rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.TwitchID, &item.Avatar, &item.HasToken, &item.Banned, &item.BanReason, &bannedAt, &item.UpdatedAt) == nil {
			if bannedAt.Valid {
				item.BannedAt = &bannedAt.Time
			}

			result = append(result, item)
		}
	}

	return result, nil
}

// DeleteUser removes a user and their tokens from the dashboard.
func (s *Service) DeleteUser(id int64) bool {
	if s.db == nil || id <= 0 {
		return false
	}

	if _, err := s.db.ExecContext(context.Background(), `DELETE FROM tokens WHERE user_id = $1`, id); err != nil {
		return false
	}

	result, err := s.db.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return false
	}

	deleted, _ := result.RowsAffected()

	return deleted > 0
}

func (s *Service) AdminUser(id int64) (AdminUser, bool) {
	if s.db == nil || id <= 0 {
		return AdminUser{}, false
	}

	var item AdminUser
	var bannedAt sql.NullTime

	err := s.db.QueryRowContext(
		context.Background(),
		`SELECT u.id, u.username, u.display_name, u.twitch_user_id, u.avatar,
		        (t.id IS NOT NULL), u.banned, u.ban_reason, u.banned_at, u.updated_at
		 FROM users u LEFT JOIN tokens t ON t.user_id = u.id
		 WHERE u.id = $1`,
		id,
	).Scan(&item.ID, &item.Username, &item.DisplayName, &item.TwitchID, &item.Avatar, &item.HasToken, &item.Banned, &item.BanReason, &bannedAt, &item.UpdatedAt)
	if err != nil {
		return AdminUser{}, false
	}

	if bannedAt.Valid {
		item.BannedAt = &bannedAt.Time
	}

	return item, true
}

func (s *Service) SetUserBan(id int64, banned bool, reason string) (AdminUser, bool) {
	if s.db == nil || id <= 0 {
		return AdminUser{}, false
	}

	_, err := s.db.ExecContext(
		context.Background(),
		`UPDATE users
		 SET banned = $2,
		     ban_reason = CASE WHEN $2 THEN $3 ELSE '' END,
		     banned_at = CASE WHEN $2 THEN now() ELSE NULL END
		 WHERE id = $1`,
		id,
		banned,
		reason,
	)
	if err != nil {
		return AdminUser{}, false
	}

	return s.AdminUser(id)
}
