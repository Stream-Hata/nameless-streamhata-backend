package chat

import (
	"context"
	"fmt"
	"os"

	"github.com/p7x-dev/nameless-streamhata-backend/llm"
)

type LLMTranslator interface {
	Translate(message string) (string, error)
}

type llmTranslator struct {
	ctx   context.Context
	model string
}

func NewLLMTranslator(ctx context.Context) LLMTranslator {
	return &llmTranslator{
		ctx: ctx,
	}
}

func (translator *llmTranslator) Translate(message string) (string, error) {
	llmClient := llm.NewGroq(os.Getenv("GROQ_API_KEY"), translator.model)
	translated, err := llmClient.Translate(translator.ctx, message)

	if err != nil {
		return "", fmt.Errorf("Ошибка перевода %w", err)
	}

	return translated, nil
}
