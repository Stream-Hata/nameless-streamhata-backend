package commands

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

var expressionPattern = regexp.MustCompile(`\{\{\s*(.+?)\s*\}\}`)

func (s *Service) evaluateExpressions(text string, message twitch.TwitchChatMessage, counter int) string {
	if !strings.Contains(text, "{{") {
		return text
	}

	return expressionPattern.ReplaceAllStringFunc(text, func(match string) string {
		code := strings.TrimSpace(expressionPattern.FindStringSubmatch(match)[1])
		if code == "" || len(code) > 512 {
			return match
		}

		env := s.expressionEnv(message, counter)
		program, err := expr.Compile(code, expr.Env(env), expr.AllowUndefinedVariables())
		if err != nil {
			return match
		}
		value, err := expr.Run(program, env)
		if err != nil {
			return match
		}

		return fmt.Sprint(value)
	})
}

func (s *Service) expressionEnv(message twitch.TwitchChatMessage, counter int) map[string]any {
	fields := strings.Fields(strings.TrimSpace(message.Text))
	args := ""
	if len(fields) > 1 {
		args = strings.Join(fields[1:], " ")
	}

	env := map[string]any{
		"user":    message.Author.Login,
		"display": message.Author.DisplayName,
		"id":      message.Author.ID,
		"message": message.Text,
		"args":    args,
		"counter": counter,
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"trim":    strings.TrimSpace,
		"replace": strings.ReplaceAll,
		"len":     func(value any) int { return len(fmt.Sprint(value)) },
		"min":     math.Min,
		"max":     math.Max,
		"clamp": func(value, low, high float64) float64 {
			return math.Max(low, math.Min(high, value))
		},
		"round": func(value float64) int64 { return int64(math.Round(value)) },
		"int": func(value any) int64 {
			parsed, _ := strconv.ParseInt(fmt.Sprint(value), 10, 64)
			return parsed
		},
		"float": func(value any) float64 {
			parsed, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
			return parsed
		},
		"str": fmt.Sprint,
	}

	for index, field := range fields[1:] {
		env["arg"+strconv.Itoa(index+1)] = field
	}

	s.mu.Lock()
	for name, variable := range s.custom {
		env[name] = variable.Value
	}
	s.mu.Unlock()

	return env
}
