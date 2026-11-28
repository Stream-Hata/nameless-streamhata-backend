package chat

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"golang.org/x/sync/singleflight"
)

type LLMTranslator interface {
	Translate(message string) (string, error)
}

type llmTranslator struct {
	ctx       context.Context
	translate func(context.Context, string) (string, error)
	inFlight  singleflight.Group
	callSlot  chan struct{}

	mu            sync.Mutex
	cache         map[string]string
	terms         *protectedTermNode
	cooldownUntil time.Time
}

func NewLLMTranslator(ctx context.Context) LLMTranslator {
	client := llm.NewFromEnv()
	return NewLLMTranslatorWithClient(ctx, client)
}

// NewLLMTranslatorWithClient shares provider credentials and cooldowns with
// other LLM features (for example, the chat jury).
func NewLLMTranslatorWithClient(ctx context.Context, client interface {
	Translate(context.Context, string) (string, error)
}) LLMTranslator {
	if client == nil {
		return nil
	}
	return newCachedTranslator(ctx, client.Translate)
}

func newCachedTranslator(ctx context.Context, translate func(context.Context, string) (string, error)) *llmTranslator {
	return &llmTranslator{ctx: ctx, translate: translate, cache: make(map[string]string), callSlot: make(chan struct{}, 1)}
}

func (translator *llmTranslator) SetProtectedTerms(terms []string) {
	root := &protectedTermNode{next: make(map[rune]*protectedTermNode)}
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		node := root
		for _, character := range strings.ToLower(term) {
			if node.next[character] == nil {
				node.next[character] = &protectedTermNode{next: make(map[rune]*protectedTermNode)}
			}
			node = node.next[character]
		}
		node.value = term
	}
	translator.mu.Lock()
	translator.terms = root
	translator.cache = make(map[string]string)
	translator.mu.Unlock()
}

func (translator *llmTranslator) NeedsTranslation(message twitch.TwitchChatMessage) bool {
	translator.mu.Lock()
	matcher := translator.terms
	translator.mu.Unlock()
	text, protected := maskProtectedTerms(textWithoutEmotes(message), matcher)
	if protected == nil {
		protected = make(map[string]string)
	}
	text = maskChatUsernames(text, protected)
	return hasForeignLetters(text) || needsRussianPunctuationCorrection(text)
}

func (translator *llmTranslator) Translate(message string) (string, error) {
	if cached, ok := translator.cached(message); ok {
		return cached, nil
	}
	if translator.isCoolingDown() {
		return message, nil
	}

	result, err, _ := translator.inFlight.Do(message, func() (any, error) {
		if cached, ok := translator.cached(message); ok {
			return cached, nil
		}
		if translator.isCoolingDown() {
			return message, nil
		}
		translator.mu.Lock()
		matcher := translator.terms
		translator.mu.Unlock()
		input, protected := maskProtectedTerms(message, matcher)
		if protected == nil {
			protected = make(map[string]string)
		}
		input = maskChatUsernames(input, protected)
		if len(protected) > 0 && !containsLetters(input) {
			translator.remember(message, message)
			return message, nil
		}
		ctx, cancel := context.WithTimeout(translator.ctx, 20*time.Second)
		defer cancel()
		select {
		case translator.callSlot <- struct{}{}:
			defer func() { <-translator.callSlot }()
		case <-ctx.Done():
			return message, nil
		}
		translated, err := translator.translate(ctx, input)
		if err != nil {
			var limit *llm.RateLimitError
			if errors.As(err, &limit) {
				cooldown := limit.RetryAfter
				if cooldown <= 0 {
					cooldown = 30 * time.Second
					detail := strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(limit.Detail))
					if strings.Contains(detail, "per day") || strings.Contains(detail, "daily") || strings.Contains(detail, "day limit") {
						cooldown = 24 * time.Hour
					}
				}
				translator.mu.Lock()
				translator.cooldownUntil = time.Now().Add(cooldown)
				translator.mu.Unlock()
			}
			return "", fmt.Errorf("Ошибка перевода: %w", err)
		}
		translator.mu.Lock()
		translator.cooldownUntil = time.Time{}
		translator.mu.Unlock()
		if len(protected) > 0 {
			var ok bool
			translated, ok = restoreProtectedTerms(translated, protected)
			if !ok {
				translator.remember(message, message)
				return message, nil
			}
		}
		if output := strings.TrimSpace(translated); validTranslation(output) {
			translator.remember(message, output)
			return output, nil
		}
		return translated, nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (translator *llmTranslator) TranslateChatMessage(message twitch.TwitchChatMessage) (string, error) {
	runes := []rune(message.Text)
	emotesAt := make(map[int]int, len(message.Emotes))
	for _, emote := range message.Emotes {
		if emote.Start >= 0 && emote.End >= emote.Start && emote.End < len(runes) {
			emotesAt[emote.Start] = emote.End
		}
	}

	var input strings.Builder
	protected := make(map[string]string)
	for index := 0; index < len(runes); {
		if end, ok := emotesAt[index]; ok {
			token := fmt.Sprintf("⟦E%d⟧", len(protected))
			protected[token] = string(runes[index : end+1])
			input.WriteString(token)
			index = end + 1
			continue
		}
		if end, ok := emojiSequenceEnd(runes, index); ok {
			token := fmt.Sprintf("⟦E%d⟧", len(protected))
			protected[token] = string(runes[index:end])
			input.WriteString(token)
			index = end
			continue
		}
		if end, ok := textEmoticonEnd(runes, index); ok {
			token := fmt.Sprintf("⟦E%d⟧", len(protected))
			protected[token] = string(runes[index:end])
			input.WriteString(token)
			index = end
			continue
		}

		if (runes[index] == '!' || runes[index] == '/') && index+1 < len(runes) && unicode.IsLetter(runes[index+1]) {
			end := index + 2
			for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '_') {
				end++
			}
			token := fmt.Sprintf("⟦E%d⟧", len(protected))
			protected[token] = string(runes[index:end])
			input.WriteString(token)
			index = end
			continue
		}

		input.WriteRune(runes[index])
		index++
	}

	if len(protected) == 0 {
		return translator.Translate(message.Text)
	}
	translated, err := translator.Translate(input.String())
	if err != nil {
		return "", err
	}
	for token, original := range protected {
		if strings.Count(translated, token) != 1 {
			return message.Text, nil
		}
		translated = strings.Replace(translated, token, original, 1)
	}
	return translated, nil
}

func emojiSequenceEnd(runes []rune, start int) (int, bool) {
	if start >= len(runes) || !unicode.IsSymbol(runes[start]) {
		return start, false
	}
	end := start + 1
	for end < len(runes) {
		character := runes[end]
		if unicode.IsSymbol(character) || character == '\u200d' || character == '\ufe0e' || character == '\ufe0f' ||
			(character >= '\U0001f3fb' && character <= '\U0001f3ff') {
			end++
			continue
		}
		break
	}
	return end, true
}

func textEmoticonEnd(runes []rune, start int) (int, bool) {
	for _, emoticon := range []string{";-)", ":-)", ":-D", ";-D", ":-P", ":-p", ":-(", ":)", ";)", ":D", ";D", ":P", ":p", ":(", "xD", "XD", "<3"} {
		candidate := []rune(emoticon)
		if start+len(candidate) > len(runes) {
			continue
		}
		match := true
		for offset, character := range candidate {
			if runes[start+offset] != character {
				match = false
				break
			}
		}
		if match {
			return start + len(candidate), true
		}
	}
	return start, false
}

func (translator *llmTranslator) isCoolingDown() bool {
	translator.mu.Lock()
	defer translator.mu.Unlock()
	return time.Now().Before(translator.cooldownUntil)
}

type protectedTermNode struct {
	next  map[rune]*protectedTermNode
	value string
}

func maskProtectedTerms(message string, root *protectedTermNode) (string, map[string]string) {
	if root == nil || len(root.next) == 0 {
		return message, nil
	}
	runes := []rune(message)
	var output strings.Builder
	protected := make(map[string]string)
	for index := 0; index < len(runes); {
		if index > 0 && isWordCharacter(runes[index-1]) {
			output.WriteRune(runes[index])
			index++
			continue
		}
		node := root
		bestEnd := -1
		for end := index; end < len(runes); end++ {
			node = node.next[unicode.ToLower(runes[end])]
			if node == nil {
				break
			}
			if node.value != "" && (end+1 == len(runes) || !isWordCharacter(runes[end+1])) {
				bestEnd = end + 1
			}
		}
		if bestEnd < 0 {
			output.WriteRune(runes[index])
			index++
			continue
		}
		token := "⟦" + strconv.Itoa(len(protected)) + "⟧"
		protected[token] = string(runes[index:bestEnd])
		output.WriteString(token)
		index = bestEnd
	}
	return output.String(), protected
}

func maskChatUsernames(message string, protected map[string]string) string {
	runes := []rune(message)
	var output strings.Builder
	for index := 0; index < len(runes); {
		if runes[index] == '⟦' {
			end := index + 1
			for end < len(runes) && runes[end] != '⟧' {
				end++
			}
			if end < len(runes) {
				output.WriteString(string(runes[index : end+1]))
				index = end + 1
				continue
			}
		}

		start := index
		if runes[index] == '@' && index+1 < len(runes) && isUsernameCharacter(runes[index+1]) {
			index += 2
			for index < len(runes) && isUsernameCharacter(runes[index]) {
				index++
			}
		} else if isUsernameCharacter(runes[index]) {
			index++
			for index < len(runes) && isUsernameCharacter(runes[index]) {
				index++
			}
			candidate := runes[start:index]
			if !looksLikeUsername(candidate) {
				output.WriteString(string(candidate))
				continue
			}
		} else {
			output.WriteRune(runes[index])
			index++
			continue
		}

		token := "⟦" + strconv.Itoa(len(protected)) + "⟧"
		protected[token] = string(runes[start:index])
		output.WriteString(token)
	}
	return output.String()
}

func isUsernameCharacter(character rune) bool {
	return unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_'
}

func looksLikeUsername(candidate []rune) bool {
	if len(candidate) == 0 {
		return false
	}
	leadingDigits := 0
	for leadingDigits < len(candidate) && unicode.IsDigit(candidate[leadingDigits]) {
		leadingDigits++
	}
	for index, character := range candidate {
		if character == '_' || (unicode.IsDigit(character) && index >= leadingDigits) || (index > 0 && unicode.IsUpper(character)) {
			return true
		}
	}
	return false
}

func restoreProtectedTerms(text string, terms map[string]string) (string, bool) {
	for token, original := range terms {
		if strings.Count(text, token) != 1 {
			return "", false
		}
		text = strings.Replace(text, token, original, 1)
	}
	return text, true
}

func containsLetters(text string) bool {
	return strings.ContainsFunc(text, unicode.IsLetter)
}

func isWordCharacter(character rune) bool {
	return unicode.IsLetter(character) || unicode.IsDigit(character)
}

func validTranslation(text string) bool {
	return text != "" && !strings.ContainsAny(text, "\r\n") &&
		!strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "!")
}

func (translator *llmTranslator) cached(message string) (string, bool) {
	translator.mu.Lock()
	defer translator.mu.Unlock()
	translation, ok := translator.cache[message]
	if !ok {
		return "", false
	}
	return translation, true
}

func (translator *llmTranslator) remember(input, output string) {
	translator.mu.Lock()
	defer translator.mu.Unlock()
	translator.cache[input] = output
}
