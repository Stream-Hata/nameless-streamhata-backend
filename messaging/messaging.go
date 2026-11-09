package messaging

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/p7x-dev/nameless-streamhata-backend/media"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/messaging/identity", s.identity)
	srv.ServeEndpoint("/rgg-sync/messaging/upload", s.upload)
	srv.ServeEndpoint("/rgg-sync/messaging/contacts", s.contacts)
	srv.ServeEndpoint("/rgg-sync/messaging/messages", s.messages)
	srv.ServeEndpoint("/rgg-sync/messaging/unread", s.unread)
	srv.ServeEndpoint("/rgg-sync/messaging/read", s.read)
	srv.ServeEndpoint("/rgg-sync/messaging/relationship", s.relationship)
	srv.ServeEndpoint("/rgg-sync/messaging/graph", s.graph)
	srv.ServeEndpoint("/rgg-sync/messaging/test-peer", s.testPeer)
	srv.ServeEndpoint("/rgg-sync/messaging/test-reply", s.testReply)
	srv.ServeEndpoint("/rgg-sync/messaging/recovery", s.recovery)
	srv.ServeEndpoint("/rgg-sync/messaging/presence", s.presence)
	srv.ServeEndpoint("/rgg-sync/messaging/conversation", s.conversation)
	srv.ServeEndpoint("/rgg-sync/messaging/messages/edit", s.editMessage)
	srv.ServeEndpoint("/rgg-sync/messaging/block", s.block)
	srv.ServeEndpoint("/rgg-sync/messaging/reactions", s.reactions)
	srv.ServeEndpoint("/rgg-sync/messaging/groups", s.groups)
	srv.ServeEndpoint("/rgg-sync/messaging/conversation/state", s.conversationState)
	srv.ServeEndpoint("/rgg-sync/games/{slug}/discussion", s.gameDiscussion)
	srv.ServeEndpoint("/rgg-sync/games/{slug}/discussion/{id}", s.gameDiscussionMessage)
	srv.ServeEndpoint("/rgg-sync/games/{slug}/discussion/{id}/like", s.gameDiscussionLike)
	srv.ServeEndpoint("/rgg-sync/games/notifications", s.gameDiscussionNotifications)
}

type Identity struct {
	Login         string          `json:"login"`
	DisplayName   string          `json:"displayName"`
	Avatar        string          `json:"avatar"`
	KeyID         string          `json:"keyId"`
	EncryptionKey json.RawMessage `json:"encryptionKey"`
	SigningKey    json.RawMessage `json:"signingKey"`
	IsTest        bool            `json:"isTest"`
	Online        bool            `json:"online"`
	Pinned        bool            `json:"pinned"`
	Archived      bool            `json:"archived"`
	ArchiveReason string          `json:"archiveReason"`
}

type Message struct {
	ID        int64           `json:"id"`
	Sender    string          `json:"sender"`
	Recipient string          `json:"recipient"`
	Envelope  json.RawMessage `json:"envelope"`
	CreatedAt time.Time       `json:"createdAt"`
	ReadAt    *time.Time      `json:"readAt,omitempty"`
	EditedAt  *time.Time      `json:"editedAt,omitempty"`
	Reactions []Reaction      `json:"reactions,omitempty"`
}

type Reaction struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

func (s *Service) identity(response http.ResponseWriter, request *http.Request) {
	userID, login, displayName, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method == http.MethodGet {
		var item Identity
		var enc, sig []byte
		err := s.db.QueryRowContext(request.Context(), `SELECT login,display_name,key_id,encryption_key,signing_key,is_test FROM streamer_identities WHERE user_id=$1`, userID).
			Scan(&item.Login, &item.DisplayName, &item.KeyID, &enc, &sig, &item.IsTest)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "identity not configured"})
			return
		}
		item.EncryptionKey, item.SigningKey = enc, sig
		writeJSON(response, http.StatusOK, item)
		return
	}
	if request.Method != http.MethodPut && request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		KeyID         string          `json:"keyId"`
		EncryptionKey json.RawMessage `json:"encryptionKey"`
		SigningKey    json.RawMessage `json:"signingKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&body); err != nil || body.KeyID == "" || len(body.EncryptionKey) == 0 || len(body.SigningKey) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid identity"})
		return
	}
	_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_identities (user_id,login,display_name,key_id,encryption_key,signing_key,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (user_id) DO UPDATE SET login=EXCLUDED.login, display_name=EXCLUDED.display_name, key_id=EXCLUDED.key_id,
		encryption_key=EXCLUDED.encryption_key, signing_key=EXCLUDED.signing_key, updated_at=now()`,
		userID, login, displayName, body.KeyID, body.EncryptionKey, body.SigningKey)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save identity"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) upload(response http.ResponseWriter, request *http.Request) {
	if _, _, _, ok := s.user(request); !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	request.Body = http.MaxBytesReader(response, request.Body, 25<<20)
	if err := request.ParseMultipartForm(25 << 20); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "failed to read file"})

		return
	}

	file, header, err := request.FormFile("file")
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "file is required"})

		return
	}
	defer func() {
		_ = file.Close()
	}()

	url, err := media.SaveImage(file, header.Filename, 2000, map[string]bool{
		".avif": true, ".bmp": true, ".gif": true, ".jpeg": true, ".jpg": true, ".png": true, ".webp": true,
		".mp3": true, ".ogg": true, ".wav": true, ".m4a": true, ".aac": true, ".flac": true, ".opus": true,
		".mp4": true, ".webm": true, ".mov": true, ".mkv": true, ".pdf": true, ".txt": true, ".zip": true,
	})
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}

	writeJSON(response, http.StatusCreated, map[string]string{"url": url})
}

func (s *Service) contacts(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	q := "%" + strings.ToLower(strings.TrimSpace(request.URL.Query().Get("q"))) + "%"
	rows, err := s.db.QueryContext(request.Context(), `SELECT i.login,i.display_name,COALESCE(u.avatar,''),i.key_id,i.encryption_key,i.signing_key,i.is_test,
		COALESCE(p.last_seen > now() - interval '2 minutes', false),
		COALESCE(s.pinned,false),
		COALESCE(s.archived, COALESCE((SELECT max(m.created_at) < now() - interval '30 days'
			FROM streamer_messages m WHERE (m.sender_id=$1 AND m.recipient_id=i.user_id)
			OR (m.sender_id=i.user_id AND m.recipient_id=$1)), false)),
		COALESCE(s.archive_reason,CASE WHEN s.archived IS NULL AND EXISTS(SELECT 1 FROM streamer_messages m
			WHERE ((m.sender_id=$1 AND m.recipient_id=i.user_id) OR (m.sender_id=i.user_id AND m.recipient_id=$1))
			AND m.created_at < now() - interval '30 days') THEN 'auto' ELSE '' END)
		FROM streamer_identities i JOIN users u ON u.id=i.user_id
		LEFT JOIN streamer_presence p ON p.user_id=i.user_id
		LEFT JOIN streamer_conversation_state s ON s.owner_id=$1 AND s.peer_id=i.user_id
		WHERE i.user_id <> $1 AND (lower(i.login) LIKE $2 OR lower(i.display_name) LIKE $2)
		ORDER BY lower(i.display_name), lower(i.login) LIMIT 100`, userID, q)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to list contacts"})
		return
	}
	defer rows.Close()
	items := make([]Identity, 0)
	for rows.Next() {
		var item Identity
		var enc, sig []byte
		if rows.Scan(&item.Login, &item.DisplayName, &item.Avatar, &item.KeyID, &enc, &sig, &item.IsTest, &item.Online, &item.Pinned, &item.Archived, &item.ArchiveReason) == nil {
			item.EncryptionKey, item.SigningKey = enc, sig
			items = append(items, item)
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) messages(response http.ResponseWriter, request *http.Request) {
	userID, login, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method == http.MethodDelete {
		id, _ := strconv.ParseInt(request.URL.Query().Get("id"), 10, 64)
		result, err := s.db.ExecContext(request.Context(), `DELETE FROM streamer_messages WHERE id=$1 AND sender_id=$2`, id, userID)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to delete message"})
			return
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "message not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if request.Method == http.MethodGet {
		peer := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("with")))
		after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
		var peerID int64
		var peerIdentity Identity
		var enc, sig []byte
		err := s.db.QueryRowContext(request.Context(), `SELECT i.user_id,i.login,i.display_name,COALESCE(u.avatar,''),i.key_id,i.encryption_key,i.signing_key,i.is_test
			FROM streamer_identities i JOIN users u ON u.id=i.user_id WHERE lower(i.login)=$1`, peer).
			Scan(&peerID, &peerIdentity.Login, &peerIdentity.DisplayName, &peerIdentity.Avatar, &peerIdentity.KeyID, &enc, &sig, &peerIdentity.IsTest)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "recipient not found"})
			return
		}
		peerIdentity.EncryptionKey, peerIdentity.SigningKey = enc, sig
		rows, err := s.db.QueryContext(request.Context(), `SELECT m.id,m.sender_id,m.recipient_id,m.envelope,m.created_at,m.read_at,m.edited_at
			FROM streamer_messages m WHERE m.id > $1 AND ((m.sender_id=$2 AND m.recipient_id=$3) OR (m.sender_id=$3 AND m.recipient_id=$2))
			AND m.created_at > COALESCE((SELECT hidden_before FROM streamer_conversation_state WHERE owner_id=$2 AND peer_id=$3),'epoch')
			ORDER BY m.id ASC LIMIT 200`, after, userID, peerID)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load messages"})
			return
		}
		defer rows.Close()
		items := make([]Message, 0)
		for rows.Next() {
			var item Message
			var senderID, recipientID int64
			var envelope []byte
			if rows.Scan(&item.ID, &senderID, &recipientID, &envelope, &item.CreatedAt, &item.ReadAt, &item.EditedAt) == nil {
				item.Envelope = envelope
				if senderID == userID {
					item.Sender = login
					item.Recipient = peer
				} else {
					item.Sender = peer
					item.Recipient = login
				}
				items = append(items, item)
			}
		}
		if len(items) > 0 {
			ids := make([]int64, 0, len(items))
			index := make(map[int64]int, len(items))
			for position, item := range items {
				ids = append(ids, item.ID)
				index[item.ID] = position
			}
			reactionRows, reactionErr := s.db.QueryContext(request.Context(), `SELECT message_id,emoji,count(*),bool_or(user_id=$2)
				FROM streamer_message_reactions WHERE message_id = ANY($1) GROUP BY message_id,emoji ORDER BY emoji`, pq.Array(ids), userID)
			if reactionErr == nil {
				defer reactionRows.Close()
				for reactionRows.Next() {
					var messageID int64
					var reaction Reaction
					if reactionRows.Scan(&messageID, &reaction.Emoji, &reaction.Count, &reaction.Mine) == nil {
						if position, ok := index[messageID]; ok {
							items[position].Reactions = append(items[position].Reactions, reaction)
						}
					}
				}
			}
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": items, "peer": peerIdentity})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Recipient string          `json:"recipient"`
		Envelope  json.RawMessage `json:"envelope"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 128<<10)).Decode(&body); err != nil || len(body.Envelope) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid message"})
		return
	}
	recipient := strings.ToLower(strings.TrimSpace(body.Recipient))
	var recipientID int64
	if err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1`, recipient).Scan(&recipientID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "recipient not found"})
		return
	}
	var blocked bool
	_ = s.db.QueryRowContext(request.Context(), `SELECT EXISTS(SELECT 1 FROM streamer_relationships
		WHERE ((owner_id=$1 AND peer_id=$2) OR (owner_id=$2 AND peer_id=$1)) AND blocked=true)`, userID, recipientID).Scan(&blocked)
	if blocked {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "dialog blocked"})
		return
	}
	var id int64
	err := s.db.QueryRowContext(request.Context(), `INSERT INTO streamer_messages (sender_id,recipient_id,envelope)
		VALUES ($1,$2,$3) RETURNING id`, userID, recipientID, body.Envelope).Scan(&id)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to send message"})
		return
	}
	_, _ = s.db.ExecContext(request.Context(), `INSERT INTO streamer_relationships (owner_id,peer_id,strength,mutual,last_interaction_at)
		VALUES ($1,$2,1,false,now()) ON CONFLICT (owner_id,peer_id) DO UPDATE SET strength=streamer_relationships.strength+1,last_interaction_at=now()`, userID, recipientID)
	_, _ = s.db.ExecContext(request.Context(), `INSERT INTO streamer_relationships (owner_id,peer_id,strength,mutual,last_interaction_at)
		VALUES ($1,$2,0,false,now()) ON CONFLICT (owner_id,peer_id) DO NOTHING`, recipientID, userID)
	_, _ = s.db.ExecContext(request.Context(), `UPDATE streamer_relationships SET mutual=true WHERE owner_id=$1 AND peer_id=$2`, recipientID, userID)
	writeJSON(response, http.StatusCreated, map[string]any{"id": id})
}

func (s *Service) conversation(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodDelete {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peer := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("with")))
	var peerID int64
	if err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1`, peer).Scan(&peerID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "dialog not found"})
		return
	}
	scope := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("scope")))
	if scope == "me" {
		result, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_conversation_state (owner_id,peer_id,hidden_before,updated_at)
			VALUES ($1,$2,now(),now()) ON CONFLICT (owner_id,peer_id) DO UPDATE SET hidden_before=now(), updated_at=now()`, userID, peerID)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to delete dialog"})
			return
		}
		_ = result
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "deleted": 0})
		return
	}
	result, err := s.db.ExecContext(request.Context(), `DELETE FROM streamer_messages
		WHERE (sender_id=$1 AND recipient_id=$2) OR (sender_id=$2 AND recipient_id=$1)`, userID, peerID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to delete dialog"})
		return
	}
	deleted, _ := result.RowsAffected()
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
}

func (s *Service) reactions(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		MessageID int64  `json:"messageId"`
		Emoji     string `json:"emoji"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil || body.MessageID == 0 || body.Emoji == "" || len([]rune(body.Emoji)) > 8 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid reaction"})
		return
	}
	var allowed bool
	err := s.db.QueryRowContext(request.Context(), `SELECT EXISTS(SELECT 1 FROM streamer_messages
		WHERE id=$1 AND (sender_id=$2 OR recipient_id=$2))`, body.MessageID, userID).Scan(&allowed)
	if err != nil || !allowed {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}
	result, err := s.db.ExecContext(request.Context(), `DELETE FROM streamer_message_reactions WHERE message_id=$1 AND user_id=$2 AND emoji=$3`, body.MessageID, userID, body.Emoji)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update reaction"})
		return
	}
	removed, _ := result.RowsAffected()
	if removed == 0 {
		_, err = s.db.ExecContext(request.Context(), `INSERT INTO streamer_message_reactions (message_id,user_id,emoji) VALUES ($1,$2,$3)`, body.MessageID, userID, body.Emoji)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update reaction"})
			return
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "added": removed == 0})
}

func (s *Service) conversationState(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPost && request.Method != http.MethodPatch {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Peer     string `json:"peer"`
		Pinned   *bool  `json:"pinned"`
		Archived *bool  `json:"archived"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Peer) == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid state"})
		return
	}
	var peerID int64
	if err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1`, strings.ToLower(strings.TrimSpace(body.Peer))).Scan(&peerID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "streamer not found"})
		return
	}
	if body.Pinned != nil {
		_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_conversation_state (owner_id,peer_id,pinned,updated_at)
			VALUES ($1,$2,$3,now())
			ON CONFLICT (owner_id,peer_id) DO UPDATE SET pinned=EXCLUDED.pinned, updated_at=now()`,
			userID, peerID, *body.Pinned)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save state"})
			return
		}
	}
	if body.Archived != nil {
		_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_conversation_state (owner_id,peer_id,archived,archived_at,archive_reason,updated_at)
			VALUES ($1,$2,$3,CASE WHEN $3 THEN now() ELSE NULL END,CASE WHEN $3 THEN 'manual' ELSE '' END,now())
			ON CONFLICT (owner_id,peer_id) DO UPDATE SET archived=EXCLUDED.archived,
			archived_at=CASE WHEN EXCLUDED.archived THEN now() ELSE NULL END,
			archive_reason=CASE WHEN EXCLUDED.archived THEN 'manual' ELSE '' END, updated_at=now()`,
			userID, peerID, *body.Archived)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save state"})
			return
		}
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) editMessage(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPatch && request.Method != http.MethodPut {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID       int64           `json:"id"`
		Envelope json.RawMessage `json:"envelope"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 128<<10)).Decode(&body); err != nil || body.ID == 0 || len(body.Envelope) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid message"})
		return
	}
	result, err := s.db.ExecContext(request.Context(), `UPDATE streamer_messages SET envelope=$1, edited_at=now()
		WHERE id=$2 AND sender_id=$3`, body.Envelope, body.ID, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to edit message"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) block(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Peer    string `json:"peer"`
		Blocked bool   `json:"blocked"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	peer := strings.ToLower(strings.TrimSpace(body.Peer))
	var peerID int64
	if err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1`, peer).Scan(&peerID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "streamer not found"})
		return
	}
	_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_relationships (owner_id,peer_id,blocked,last_interaction_at)
		VALUES ($1,$2,$3,now()) ON CONFLICT (owner_id,peer_id) DO UPDATE SET blocked=EXCLUDED.blocked, last_interaction_at=now()`, userID, peerID, body.Blocked)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update block"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true, "blocked": body.Blocked})
}

func (s *Service) presence(response http.ResponseWriter, request *http.Request) {
	userID, login, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method == http.MethodPost {
		var body struct {
			Typing string `json:"typing"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body)
		peer := strings.ToLower(strings.TrimSpace(body.Typing))
		if peer == "" {
			_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_presence (user_id,last_seen) VALUES ($1,now())
				ON CONFLICT (user_id) DO UPDATE SET last_seen=now()`, userID)
			if err != nil {
				writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update presence"})
				return
			}
			writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_presence (user_id,last_seen,typing_peer,typing_until) VALUES ($1,now(),$2,now()+interval '4 seconds')
			ON CONFLICT (user_id) DO UPDATE SET last_seen=now(), typing_peer=EXCLUDED.typing_peer, typing_until=EXCLUDED.typing_until`, userID, peer)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update presence"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peer := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("with")))
	var lastSeen time.Time
	var typingPeer string
	var typingUntil sql.NullTime
	err := s.db.QueryRowContext(request.Context(), `SELECT p.last_seen,p.typing_peer,p.typing_until
		FROM streamer_presence p JOIN streamer_identities i ON i.user_id=p.user_id
		WHERE lower(i.login)=$1`, peer).Scan(&lastSeen, &typingPeer, &typingUntil)
	if err != nil && err != sql.ErrNoRows {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load presence"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"online": err == nil && time.Since(lastSeen) < 2*time.Minute,
		"typing": err == nil && typingPeer == login && typingUntil.Valid && typingUntil.Time.After(time.Now()),
	})
}

func (s *Service) recovery(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		var blob []byte
		err := s.db.QueryRowContext(request.Context(), `SELECT blob FROM streamer_identity_recovery WHERE user_id=$1`, userID).Scan(&blob)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "recovery not configured"})
			return
		}
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write(blob)
	case http.MethodPut, http.MethodPost:
		var blob json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 256<<10)).Decode(&blob); err != nil || len(blob) == 0 {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid recovery blob"})
			return
		}
		_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_identity_recovery (user_id,blob,updated_at) VALUES ($1,$2,now())
			ON CONFLICT (user_id) DO UPDATE SET blob=EXCLUDED.blob, updated_at=now()`, userID, blob)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save recovery"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) testPeer(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		KeyID         string          `json:"keyId"`
		EncryptionKey json.RawMessage `json:"encryptionKey"`
		SigningKey    json.RawMessage `json:"signingKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&body); err != nil || body.KeyID == "" || len(body.EncryptionKey) == 0 || len(body.SigningKey) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid identity"})
		return
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create test peer"})
		return
	}
	login := fmt.Sprintf("test_%d_%s", userID, hex.EncodeToString(random))
	displayName := "Тестовый стример " + hex.EncodeToString(random[:2])
	twitchID := -time.Now().UnixNano()
	var peerID int64
	if err := s.db.QueryRowContext(request.Context(), `INSERT INTO users (username,display_name,twitch_user_id) VALUES ($1,$2,$3) RETURNING id`, login, displayName, twitchID).Scan(&peerID); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create test peer"})
		return
	}
	_, err := s.db.ExecContext(request.Context(), `INSERT INTO streamer_identities (user_id,login,display_name,key_id,encryption_key,signing_key,is_test,test_owner_id)
		VALUES ($1,$2,$3,$4,$5,$6,true,$7)`, peerID, login, displayName, body.KeyID, body.EncryptionKey, body.SigningKey, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save test identity"})
		return
	}
	writeJSON(response, http.StatusCreated, map[string]any{"login": login, "displayName": displayName, "isTest": true})
}

func (s *Service) testReply(response http.ResponseWriter, request *http.Request) {
	userID, login, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Peer     string          `json:"peer"`
		Envelope json.RawMessage `json:"envelope"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 128<<10)).Decode(&body); err != nil || len(body.Envelope) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid reply"})
		return
	}
	peer := strings.ToLower(strings.TrimSpace(body.Peer))
	var peerID int64
	err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1 AND is_test=true AND test_owner_id=$2`, peer, userID).Scan(&peerID)
	if err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "test peer not found"})
		return
	}
	var id int64
	if err := s.db.QueryRowContext(request.Context(), `INSERT INTO streamer_messages (sender_id,recipient_id,envelope) VALUES ($1,$2,$3) RETURNING id`, peerID, userID, body.Envelope).Scan(&id); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save reply"})
		return
	}
	_, _ = s.db.ExecContext(request.Context(), `INSERT INTO streamer_relationships (owner_id,peer_id,strength,mutual,last_interaction_at)
		VALUES ($1,$2,1,true,now()) ON CONFLICT (owner_id,peer_id) DO UPDATE SET strength=streamer_relationships.strength+1,mutual=true,last_interaction_at=now()`, peerID, userID)
	writeJSON(response, http.StatusCreated, map[string]any{"id": id, "sender": login})
}

func (s *Service) unread(response http.ResponseWriter, request *http.Request) {
	userID, login, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	rows, err := s.db.QueryContext(request.Context(), `SELECT m.id,i.login,COALESCE(NULLIF(i.display_name,''),i.login),COALESCE(u.avatar,''),m.envelope,m.created_at,i.key_id,i.encryption_key,i.signing_key
		FROM streamer_messages m JOIN streamer_identities i ON i.user_id=m.sender_id JOIN users u ON u.id=m.sender_id
		WHERE m.recipient_id=$1 AND m.read_at IS NULL ORDER BY m.id ASC LIMIT 50`, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load unread"})
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var sender, display, avatar, keyID string
		var envelope, encryptionKey, signingKey []byte
		var created time.Time
		if rows.Scan(&id, &sender, &display, &avatar, &envelope, &created, &keyID, &encryptionKey, &signingKey) == nil {
			items = append(items, map[string]any{"id": id, "sender": sender, "displayName": display, "avatar": avatar, "envelope": json.RawMessage(envelope), "createdAt": created, "recipient": login, "keyId": keyID, "encryptionKey": json.RawMessage(encryptionKey), "signingKey": json.RawMessage(signingKey)})
		}
	}
	var unreadTotal int
	_ = s.db.QueryRowContext(request.Context(), `SELECT count(*) FROM streamer_messages WHERE recipient_id=$1 AND read_at IS NULL`, userID).Scan(&unreadTotal)
	writeJSON(response, http.StatusOK, map[string]any{"items": items, "count": unreadTotal})
}

func (s *Service) read(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	for _, id := range body.IDs {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE streamer_messages SET read_at=now() WHERE id=$1 AND recipient_id=$2 AND read_at IS NULL`, id, userID)
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) graph(response http.ResponseWriter, request *http.Request) {
	userID, login, displayName, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}

	rows, err := s.db.QueryContext(request.Context(), `
		SELECT u.id, u.username, COALESCE(NULLIF(u.display_name, ''), u.username), COALESCE(u.avatar, ''),
			COALESCE(r.strength, 0), COALESCE(r.mutual, false), COALESCE(r.blocked, false),
			(SELECT count(*) FROM streamer_messages m
			 WHERE (m.sender_id = $1 AND m.recipient_id = u.id)
			    OR (m.sender_id = u.id AND m.recipient_id = $1)),
			COALESCE(pw.total_earned, 0),
			EXISTS(SELECT 1 FROM streamer_identities i WHERE i.user_id = u.id)
		FROM users u
		LEFT JOIN streamer_relationships r ON r.owner_id = $1 AND r.peer_id = u.id
		LEFT JOIN points_wallet pw ON pw.user_id = u.twitch_user_id::text
		WHERE u.id <> $1 AND (
			r.peer_id IS NOT NULL
			OR pw.user_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM streamer_identities i WHERE i.user_id = u.id)
			OR EXISTS(
				SELECT 1 FROM streamer_messages m
				WHERE (m.sender_id = $1 AND m.recipient_id = u.id)
				   OR (m.sender_id = u.id AND m.recipient_id = $1)
			)
		)
		ORDER BY lower(u.display_name), lower(u.username) LIMIT 200`, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load graph"})
		return
	}
	defer rows.Close()

	nodes := make([]map[string]any, 0)
	for rows.Next() {
		var peerID int64
		var peerLogin, peerDisplay, peerAvatar string
		var strength, messages, points int
		var mutual, blocked, hasIdentity bool

		if rows.Scan(&peerID, &peerLogin, &peerDisplay, &peerAvatar, &strength, &mutual, &blocked, &messages, &points, &hasIdentity) != nil {
			continue
		}

		activity := messages + points/100
		if activity > strength {
			strength = activity
		}

		label := "Незнакомец"
		switch {
		case blocked:
			label = "Заблокирован"
		case mutual && strength >= 5:
			label = "Друг"
		case strength > 0:
			label = "Знакомый"
		}
		nodes = append(nodes, map[string]any{
			"login": peerLogin, "displayName": peerDisplay, "avatar": peerAvatar, "strength": strength,
			"mutual": mutual, "blocked": blocked, "label": label, "hasIdentity": hasIdentity,
		})
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"center": map[string]any{"login": login, "displayName": displayName},
		"nodes":  nodes,
	})
}

func (s *Service) relationship(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	peer := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("with")))
	var peerID int64
	if err := s.db.QueryRowContext(request.Context(), `SELECT user_id FROM streamer_identities WHERE lower(login)=$1`, peer).Scan(&peerID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "streamer not found"})
		return
	}
	var strength int
	var mutual, blocked bool
	err := s.db.QueryRowContext(request.Context(), `SELECT strength,mutual,blocked FROM streamer_relationships WHERE owner_id=$1 AND peer_id=$2`, userID, peerID).
		Scan(&strength, &mutual, &blocked)
	if err != nil && err != sql.ErrNoRows {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load relationship"})
		return
	}
	var reverseMutual bool
	_ = s.db.QueryRowContext(request.Context(), `SELECT mutual FROM streamer_relationships WHERE owner_id=$1 AND peer_id=$2`, peerID, userID).Scan(&reverseMutual)
	mutual = mutual && reverseMutual
	label := "Незнакомец"
	switch {
	case blocked:
		label = "Заблокирован"
	case mutual && strength >= 5:
		label = "Друг"
	case strength > 0:
		label = "Знакомый"
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"strength": strength, "mutual": mutual, "blocked": blocked, "label": label,
	})
}

func (s *Service) user(request *http.Request) (int64, string, string, bool) {
	cookie, err := request.Cookie("e-hub-refresh-token")
	if err != nil || cookie.Value == "" {
		return 0, "", "", false
	}
	var id int64
	var login, display string
	err = s.db.QueryRowContext(request.Context(), `SELECT u.id,u.username,COALESCE(NULLIF(u.display_name,''),u.username)
		FROM tokens t JOIN users u ON u.id=t.user_id WHERE t.refresh_token=$1 AND u.banned=false LIMIT 1`, cookie.Value).
		Scan(&id, &login, &display)
	return id, login, display, err == nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
