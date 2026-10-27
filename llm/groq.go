// Package llm — классификатор советов и генератор вердиктов (Groq, HTTP).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	groqURL          = "https://api.groq.com/openai/v1/chat/completions"
	defaultGroqModel = "openai/gpt-oss-20b"
	classifierPrompt = "Ты классификатор сообщений Twitch-чата.\n\nОпредели, является ли сообщение советом или рекомендацией ДЛЯ СТРИМЕРА.\n\nTRUE:\n- рекомендация, что стримеру сделать;\n- рекомендация, чего стримеру не делать;\n- предложение конкретного действия;\n- совет по игре;\n- совет по настройкам;\n- совет по OBS;\n- совет по технике;\n- совет по контенту или стриму.\n\nFALSE:\n- вопросы;\n- шутки;\n- мемы;\n- приветствия;\n- реакции;\n- оскорбления;\n- обычные утверждения;\n- сообщения без рекомендации;\n- советы другим зрителям, а не стримеру.\n\nОцени также уверенность классификации.\n\nconfidence:\n0.0 = практически нет уверенности;\n1.0 = практически полная уверенность.\n\nВерни ТОЛЬКО JSON без markdown:\n\n{\"is_advice\":true,\"confidence\":0.95}\n\nили:\n\n{\"is_advice\":false,\"confidence\":0.98}"
	verdictPrompt    = "You generate a short Russian verdict for a Twitch chat jury.\n\nThe jury voted on whether the streamer's viewer advice should be trusted.\n\nThe approval percentage is calculated from YEA / (YEA + NEI).\n\nWrite ONE short sentence in Russian.\n\nThe sentence must:\n- mention the viewer username;\n- reflect the approval percentage;\n- sound natural and slightly playful;\n- not invent facts;\n- not change the percentage;\n- not mention that you are an AI;\n- not use markdown;\n- not use quotes.\n\nExamples:\n\n\"Совету зрителя nameless можно доверить — доверие 72%.\"\n\n\"Чат почти единогласно доверяет совету nameless — доверие 91%.\"\n\n\"Совет nameless вызвал сомнения у чата — доверие 38%.\"\n\n\"Чат практически забраковал совет nameless — доверие 11%.\""
)

var (
	jsonBlockPattern = regexp.MustCompile(`\{[\s\S]*\}`)
	jsonFenceRegexp  = regexp.MustCompile("```(?:json)?")
)

// Classification — результат классификации сообщения чата.
type Classification struct {
	IsAdvice   bool
	Confidence float64
}

// Classifier определяет, является ли сообщение советом.
type Classifier interface {
	Classify(ctx context.Context, message string) (Classification, error)
}

// VerdictInput — данные для генерации вердикта.
type VerdictInput struct {
	Message      string
	Author       string
	Yea          int
	Nei          int
	ApprovalRate float64
}

type Verdict struct {
	Text string
}

// VerdictGenerator формирует короткий вердикт по итогам голосования.
type VerdictGenerator interface {
	Generate(ctx context.Context, input VerdictInput) (Verdict, error)
}

// DisabledClassifier — заглушка, когда GROQ_API_KEY не задан.
type DisabledClassifier struct{}

func (DisabledClassifier) Classify(context.Context, string) (Classification, error) {
	return Classification{}, nil
}

// FallbackVerdictGenerator строит вердикт без LLM.
type FallbackVerdictGenerator struct{}

func (FallbackVerdictGenerator) Generate(_ context.Context, input VerdictInput) (Verdict, error) {
	percent := int(input.ApprovalRate*100 + 0.5)

	return Verdict{Text: fmt.Sprintf("Совету зрителя %s доверие %d%%.", input.Author, percent)}, nil
}

// Groq — клиент Groq Chat Completions (классификатор и вердикты).
type Groq struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewGroq(apiKey, model string) *Groq {
	if model == "" {
		model = defaultGroqModel
	}

	return &Groq{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (g *Groq) configured() bool {
	return strings.TrimSpace(g.apiKey) != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model"`
	Temperature         float64       `json:"temperature"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	Messages            []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (g *Groq) complete(
	ctx context.Context,
	temperature float64,
	maxTokens int,
	System string,
	tools []chatMessage,
) (string, error) {
	if !g.configured() {
		return "", errors.New("groq: API key is not configured")
	}

	body, err := json.Marshal(chatRequest{
		Model:               g.model,
		Temperature:         temperature,
		MaxCompletionTokens: maxTokens,
		Messages:            append([]chatMessage{{Role: "system", Content: System}}, tools...),
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, groqURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}

	request.Header.Set("Authorization", "Bearer "+g.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := g.http.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq: completion returned %d", response.StatusCode)
	}

	var parsed chatResponse

	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return "", err
	}

	if len(parsed.Choices) == 0 {
		return "", errors.New("groq: empty choices")
	}

	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// Classify классифицирует сообщение как совет/не совет.
func (g *Groq) Classify(ctx context.Context, message string) (Classification, error) {
	if !g.configured() {
		return Classification{}, nil
	}

	content, err := g.complete(ctx, 0, 512, classifierPrompt, []chatMessage{
		{Role: "user", Content: message},
	})
	if err != nil {
		return Classification{}, err
	}

	if content == "" {
		return Classification{}, nil
	}

	return parseClassification(content), nil
}

func (g *Groq) Translate(ctx context.Context, text, language string) (string, error) {
	if text == "" {
		return "", nil
	}

	target := "Russian"
	if language == "en" {
		target = "English"
	}

	content, err := g.complete(ctx, 0, 1200,
		fmt.Sprintf("Translate the following lot metadata to %s. Keep game, movie, music and DLC proper names unchanged. Return only the translation, without quotes or markdown.", target),
		[]chatMessage{{Role: "user", Content: text}})
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(content), nil
}

func (g *Groq) TranslateJSON(ctx context.Context, payload, language string) (string, error) {
	target := "Russian"
	if language == "en" {
		target = "English"
	}

	content, err := g.complete(ctx, 0, 1800,
		fmt.Sprintf("Translate all JSON string values to %s. Keep JSON keys unchanged. Preserve game, movie, music, company, platform and DLC proper names in their official localized form when known. Return only valid JSON without markdown fences.", target),
		[]chatMessage{{Role: "user", Content: payload}})
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(content), nil
}

func parseClassification(content string) Classification {
	cleaned := jsonFenceRegexp.ReplaceAllString(content, "")
	cleaned = strings.Trim(cleaned, "` \n")

	if match := jsonBlockPattern.FindString(cleaned); match != "" {
		cleaned = match
	}

	var payload struct {
		IsAdvice   bool    `json:"is_advice"`
		Confidence float64 `json:"confidence"`
	}

	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		return Classification{}
	}

	confidence := payload.Confidence

	if confidence < 0 {
		confidence = 0
	}

	if confidence > 1 {
		confidence = 1
	}

	return Classification{IsAdvice: payload.IsAdvice, Confidence: confidence}
}

// Toxicity — результат AI-модерации сообщения.
type Toxicity struct {
	Toxic  bool    `json:"toxic"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

const toxicityPrompt = "Ты модератор Twitch-чата. Оцени сообщение по шкале токсичности: оскорбления, травля, угрозы, hate speech, спам-скам. Верни ТОЛЬКО JSON без markdown: {\"toxic\":true,\"score\":0.87,\"reason\":\"оскорбление\"} или {\"toxic\":false,\"score\":0.05,\"reason\":\"\"}. score от 0 до 1."

// ClassifyToxicity проверяет сообщение на токсичность.
func (g *Groq) ClassifyToxicity(ctx context.Context, message string) (Toxicity, error) {
	if !g.configured() {
		return Toxicity{}, nil
	}

	content, err := g.complete(ctx, 0, 256, toxicityPrompt, []chatMessage{{Role: "user", Content: message}})
	if err != nil {
		return Toxicity{}, err
	}

	cleaned := jsonFenceRegexp.ReplaceAllString(content, "")
	cleaned = strings.Trim(cleaned, "` \n")

	if match := jsonBlockPattern.FindString(cleaned); match != "" {
		cleaned = match
	}

	var parsed Toxicity

	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return Toxicity{}, err
	}

	if parsed.Score < 0 {
		parsed.Score = 0
	}

	if parsed.Score > 1 {
		parsed.Score = 1
	}

	return parsed, nil
}

// GenerateStreamSummary делает короткое резюме прошедшего эфира.
func (g *Groq) GenerateStreamSummary(ctx context.Context, facts string) (string, error) {
	if !g.configured() {
		return "", errors.New("groq: not configured")
	}

	const prompt = "Ты ассистент стримера. На основе фактов сессии напиши 2-3 предложения итогов на русском: как прошёл эфир, что было самым заметным. Без markdown, без выдуманных фактов. Используй только переданные данные."

	return g.complete(ctx, 0.5, 120, prompt, []chatMessage{{Role: "user", Content: facts}})
}

// SuggestTitles предлагает заголовки стрима.
func (g *Groq) SuggestTitles(ctx context.Context, facts string) ([]string, error) {
	if !g.configured() {
		return nil, errors.New("groq: not configured")
	}

	const prompt = "Ты помощник стримера. Придумай 3 коротких заголовка для трансляции на русском (до 70 символов), цепких и без кликбейта. Верни ТОЛЬКО JSON: {\"titles\":[\"…\",\"…\",\"…\"]}."

	content, err := g.complete(ctx, 0.8, 160, prompt, []chatMessage{{Role: "user", Content: facts}})
	if err != nil {
		return nil, err
	}

	cleaned := jsonFenceRegexp.ReplaceAllString(content, "")
	cleaned = strings.Trim(cleaned, "` \n")

	var payload struct {
		Titles []string `json:"titles"`
	}

	if err := json.Unmarshal([]byte(cleaned), &payload); err != nil {
		if match := jsonBlockPattern.FindString(cleaned); match != "" {
			if err := json.Unmarshal([]byte(match), &payload); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	result := make([]string, 0, len(payload.Titles))

	for _, title := range payload.Titles {
		if trimmed := strings.TrimSpace(title); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result, nil
}

// Generate формирует короткий вердикт по итогам голосования.
func (g *Groq) Generate(ctx context.Context, input VerdictInput) (Verdict, error) {
	fallback, _ := FallbackVerdictGenerator{}.Generate(ctx, input)

	if !g.configured() {
		return fallback, nil
	}

	payload, err := json.Marshal(map[string]any{
		"advice":           input.Message,
		"viewer":           input.Author,
		"yea":              input.Yea,
		"nei":              input.Nei,
		"approval_rate":    input.ApprovalRate,
		"approval_percent": int(input.ApprovalRate*100 + 0.5),
	})
	if err != nil {
		return fallback, nil
	}

	text, err := g.complete(ctx, 0.7, 64, verdictPrompt, []chatMessage{
		{Role: "user", Content: string(payload)},
	})
	if err != nil || text == "" {
		return fallback, err
	}

	return Verdict{Text: strings.Trim(text, "\"'")}, nil
}
