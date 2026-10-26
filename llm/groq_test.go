package llm

import (
	"context"
	"testing"
)

func TestParseClassificationPlainJSON(t *testing.T) {
	result := parseClassification(`{"is_advice":true,"confidence":0.95}`)

	if !result.IsAdvice || result.Confidence != 0.95 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseClassificationWithFenceAndNoise(t *testing.T) {
	result := parseClassification("Вот ответ:\n```json\n{\"is_advice\":false,\"confidence\":1.4}\n```\n")

	if result.IsAdvice || result.Confidence != 1 {
		t.Fatalf("expected clamped confidence, got %+v", result)
	}
}

func TestParseClassificationInvalid(t *testing.T) {
	result := parseClassification("не могу ответить")

	if result.IsAdvice || result.Confidence != 0 {
		t.Fatalf("expected zero classification, got %+v", result)
	}
}

func TestFallbackVerdict(t *testing.T) {
	verdict, err := FallbackVerdictGenerator{}.Generate(context.Background(), VerdictInput{
		Author:       "nameless",
		ApprovalRate: 0.72,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Text != "Совету зрителя nameless доверие 72%." {
		t.Fatalf("unexpected verdict: %q", verdict.Text)
	}
}
