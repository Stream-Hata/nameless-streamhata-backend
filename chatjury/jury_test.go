package chatjury

import (
	"context"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type fakeClassifier struct {
	advice     bool
	confidence float64
}

func (f fakeClassifier) Classify(context.Context, string) (llm.Classification, error) {
	return llm.Classification{IsAdvice: f.advice, Confidence: f.confidence}, nil
}

type fakeVerdicts struct {
	text string
}

func (f fakeVerdicts) Generate(context.Context, llm.VerdictInput) (llm.Verdict, error) {
	return llm.Verdict{Text: f.text}, nil
}

func testMessage(id, author, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID: id,
		Author:    twitch.ChatUser{ID: author, Login: author, DisplayName: author},
		Text:      text,
		SentAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func TestJuryVotingLifecycle(t *testing.T) {
	service := NewService(fakeVerdicts{text: "Совету доверие 66%."})
	service.settings.DurationSeconds = 1

	started := service.Start(ChatJuryMessage{
		ID:     "m1",
		Author: Author{ID: "1", Username: "nameless", DisplayName: "nameless"},
		Text:   "спрячьтесь за бочкой",
	})

	if started == nil {
		t.Fatal("expected jury to start")
	}

	if service.Start(ChatJuryMessage{ID: "m2"}) != nil {
		t.Fatal("second jury must not start while voting")
	}

	if !service.Vote("1", true) || !service.Vote("2", true) || !service.Vote("3", false) {
		t.Fatal("expected votes to be accepted")
	}

	if service.Vote("1", true) {
		t.Fatal("duplicate vote must be rejected")
	}

	if !service.Vote("3", true) {
		t.Fatal("vote change must be accepted")
	}

	current := service.GetCurrentPublic()

	if current == nil || current.Votes.Yea != 3 || current.Votes.Nei != 0 {
		t.Fatalf("unexpected current public: %+v", current)
	}

	deadline := time.Now().Add(3 * time.Second)

	for service.GetLastResult() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	result := service.GetLastResult()

	if result == nil {
		t.Fatal("expected result after timeout")
	}

	if result.Total != 3 || result.Yea != 3 || result.Status != statusFinished {
		t.Fatalf("unexpected result: %+v", result)
	}

	if result.Approval < 0.99 || result.Approval > 1 {
		t.Fatalf("unexpected approval: %v", result.Approval)
	}

	for result.Verdict == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)

		result = service.GetLastResult()
	}

	if result.Verdict == nil {
		t.Fatal("expected verdict to be generated")
	}
}

func TestSettingsValidation(t *testing.T) {
	service := NewService(nil)

	zero := 0
	tooBig := 1.5
	ok := 0.6
	duration := 45

	if _, err := service.UpdateSettings(SettingsPatch{DurationSeconds: &zero}); err == nil {
		t.Fatal("expected duration validation error")
	}

	if _, err := service.UpdateSettings(SettingsPatch{MinConfidence: &tooBig}); err == nil {
		t.Fatal("expected confidence validation error")
	}

	settings, err := service.UpdateSettings(SettingsPatch{
		DurationSeconds: &duration,
		MinConfidence:   &ok,
	})

	if err != nil || settings.DurationSeconds != 45 || settings.MinConfidence != 0.6 {
		t.Fatalf("unexpected settings: %+v (%v)", settings, err)
	}
}

func TestSchedulerStartsJuryForAdvice(t *testing.T) {
	chatService := chat.NewService(10)
	service := NewService(fakeVerdicts{text: "ok"})
	scheduler := NewScheduler(service, fakeClassifier{advice: true, confidence: 0.9}, chatService)
	_ = scheduler

	chatService.Receive(testMessage("a1", "nameless", "спрячьтесь за бочкой"))

	deadline := time.Now().Add(2 * time.Second)

	for service.GetCurrentPublic() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if service.GetCurrentPublic() == nil {
		t.Fatal("expected jury to start from advice message")
	}

	chatService.Receive(testMessage("a2", "voter", "!yea"))

	for service.GetCurrentPublic().Votes.Yea == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if service.GetCurrentPublic().Votes.Yea != 1 {
		t.Fatalf("expected yea vote, got %+v", service.GetCurrentPublic())
	}
}

func TestSchedulerIgnoresLowConfidence(t *testing.T) {
	chatService := chat.NewService(10)
	service := NewService(nil)
	_ = NewScheduler(service, fakeClassifier{advice: true, confidence: 0.2}, chatService)

	chatService.Receive(testMessage("b1", "nameless", "может бочку?"))

	time.Sleep(100 * time.Millisecond)

	if service.GetCurrentPublic() != nil {
		t.Fatal("low-confidence advice must not start jury")
	}
}
