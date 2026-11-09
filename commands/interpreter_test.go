package commands

import (
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func TestEvaluateExpressions(t *testing.T) {
	service := &Service{custom: map[string]Variable{}}
	message := twitch.TwitchChatMessage{Text: "!calc 7 5"}
	message.Author.Login = "viewer"
	message.Author.DisplayName = "Viewer"

	cases := map[string]string{
		"{{ 2 + 2 * 3 }}":   "8",
		"{{ user }}":        "viewer",
		"{{ args }}":        "7 5",
		"{{ upper(user) }}": "VIEWER",
		"{{ arg1 }}":        "7",
	}

	for expression, expected := range cases {
		if actual := service.evaluateExpressions(expression, message, 0); actual != expected {
			t.Fatalf("%s: expected %q, got %q", expression, expected, actual)
		}
	}
}
