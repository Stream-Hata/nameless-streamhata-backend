package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSeverityForVerdict(t *testing.T) {
	tests := map[string]string{
		"infected":       "critical",
		"malware":        "critical",
		"tampered":       "critical",
		"fake":           "critical",
		"dangerous_code": "critical",
		"suspicious":     "warning",
		"blocked":        "warning",
		"clean":          "info",
	}

	for verdict, want := range tests {
		if got := severityForVerdict(verdict); got != want {
			t.Fatalf("severityForVerdict(%q) = %q, want %q", verdict, got, want)
		}
	}
}

func TestShouldAutoBan(t *testing.T) {
	if !shouldAutoBan("infected") {
		t.Fatal("infected must auto-ban")
	}
	if !shouldAutoBan("dangerous_code") {
		t.Fatal("dangerous code must auto-ban")
	}
	if shouldAutoBan("suspicious") {
		t.Fatal("suspicious must not auto-ban")
	}
	if shouldAutoBan("clean") {
		t.Fatal("clean must not auto-ban")
	}
}

func TestNormalizeInput(t *testing.T) {
	input := CreateInput{
		Kind:    " FILE ",
		Verdict: " INFECTED ",
		Report:  json.RawMessage(`{"engine":"clamav"}`),
	}

	if err := normalizeInput(&input); err != nil {
		t.Fatal(err)
	}
	if input.Kind != "file" {
		t.Fatalf("kind = %q", input.Kind)
	}
	if input.Verdict != "infected" {
		t.Fatalf("verdict = %q", input.Verdict)
	}
	if input.Severity != "critical" {
		t.Fatalf("severity = %q", input.Severity)
	}
	if input.ReviewAfter != 24*time.Hour {
		t.Fatalf("review window = %s", input.ReviewAfter)
	}
}

func TestNormalizeInputRejectsUnknownKind(t *testing.T) {
	input := CreateInput{Kind: "executable", Verdict: "infected"}
	if err := normalizeInput(&input); err == nil {
		t.Fatal("expected invalid kind error")
	}
}

func TestNormalizeInputRejectsInvalidReport(t *testing.T) {
	input := CreateInput{Kind: "file", Verdict: "infected", Report: json.RawMessage(`{`)}
	if err := normalizeInput(&input); err == nil {
		t.Fatal("expected invalid report error")
	}
}

func TestValidResolveAction(t *testing.T) {
	for _, action := range []string{ActionConfirmFraud, ActionFalsePositive, ActionClose} {
		if !validResolveAction(action) {
			t.Fatalf("%q must be valid", action)
		}
	}
	if validResolveAction("delete_everything") {
		t.Fatal("unexpected action accepted")
	}
}

func TestTelegramText(t *testing.T) {
	incident := Incident{
		ID:             42,
		Kind:           "file",
		Verdict:        "infected",
		SenderLogin:    "scammer",
		ChatType:       "dm",
		ChatID:         "7",
		FileName:       "update.apk",
		FileHash:       "abc123",
		Reason:         "malware detected",
		ReviewDeadline: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
	}

	text := TelegramText(incident)

	for _, part := range []string{"#42", "scammer", "update.apk", "abc123", "malware detected"} {
		if !strings.Contains(text, part) {
			t.Fatalf("telegram text missing %q: %s", part, text)
		}
	}
}

type fakeHashStore struct {
	record HashRecord
	found  bool
	saved  []HashRecord
}

func (s *fakeHashStore) Lookup(context.Context, string) (HashRecord, bool, error) {
	return s.record, s.found, nil
}

func (s *fakeHashStore) Save(_ context.Context, record HashRecord) error {
	s.saved = append(s.saved, record)
	s.record = record
	s.found = true

	return nil
}

type fakeClam struct {
	result ClamResult
	err    error
}

func (f fakeClam) Scan(context.Context, []byte) (ClamResult, error) {
	return f.result, f.err
}

type fakeReputation struct {
	result ReputationResult
	err    error
}

func (f fakeReputation) Lookup(context.Context, string) (ReputationResult, error) {
	return f.result, f.err
}

func TestScannerBlocksClamAVDetection(t *testing.T) {
	store := &fakeHashStore{}
	scanner := &Scanner{
		hashes: store,
		clam:   fakeClam{result: ClamResult{Detected: true, Signature: "Eicar-Test-Signature"}},
		now:    time.Now,
	}

	report, err := scanner.Scan(context.Background(), []byte("eicar"), ScanMeta{FileName: "test.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanBlocked {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanBlocked)
	}
	if report.TrustedDetections != 1 {
		t.Fatalf("trusted detections = %d", report.TrustedDetections)
	}
	if len(store.saved) != 1 || store.saved[0].Verdict != "malicious" {
		t.Fatalf("malicious result was not cached: %+v", store.saved)
	}
}

func TestScannerBlocksTwoTrustedDetections(t *testing.T) {
	scanner := &Scanner{
		clam:       fakeClam{},
		reputation: fakeReputation{result: ReputationResult{RawDetections: 2, TrustedDetections: 2}},
		now:        time.Now,
	}

	report, err := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanBlocked {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanBlocked)
	}
}

func TestScannerBlocksExecutableWithOneTrustedDetection(t *testing.T) {
	scanner := &Scanner{
		clam:       fakeClam{},
		reputation: fakeReputation{result: ReputationResult{RawDetections: 1, TrustedDetections: 1}},
		now:        time.Now,
	}

	report, _ := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "setup.exe"})
	if report.Verdict != ScanBlocked {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanBlocked)
	}
}

func TestScannerMarksRegularFileSuspicious(t *testing.T) {
	scanner := &Scanner{
		clam:       fakeClam{},
		reputation: fakeReputation{result: ReputationResult{RawDetections: 1, TrustedDetections: 1}},
		now:        time.Now,
	}

	report, _ := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "notes.txt"})
	if report.Verdict != ScanSuspicious {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanSuspicious)
	}
}

func TestScannerReturnsCleanDegradedWhenReputationFails(t *testing.T) {
	scanner := &Scanner{
		clam:       fakeClam{},
		reputation: fakeReputation{err: errors.New("offline")},
		now:        time.Now,
	}

	report, err := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanCleanDegraded {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanCleanDegraded)
	}
}

func TestScannerUsesMaliciousCache(t *testing.T) {
	store := &fakeHashStore{
		found:  true,
		record: HashRecord{Verdict: "malicious", Reason: "known malware"},
	}
	scanner := &Scanner{hashes: store, clam: fakeClam{}, now: time.Now}

	report, err := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanBlocked || !report.Cached {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestLinkCheckerRejectsPrivateHost(t *testing.T) {
	checker := &LinkChecker{}
	report, err := checker.Check(context.Background(), "http://127.0.0.1/admin")
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != LinkMalicious {
		t.Fatalf("verdict = %q", report.Verdict)
	}
}

func TestLinkCheckerRejectsUnsafeScheme(t *testing.T) {
	checker := &LinkChecker{}
	report, _ := checker.Check(context.Background(), "javascript:alert(1)")
	if report.Verdict != LinkMalicious {
		t.Fatalf("verdict = %q", report.Verdict)
	}
}

func TestLinkCheckerMarksDownload(t *testing.T) {
	checker := &LinkChecker{}
	report, _ := checker.Check(context.Background(), "https://example.com/releases/app.apk")
	if report.Verdict != LinkDownload {
		t.Fatalf("verdict = %q", report.Verdict)
	}
}

func TestLinkCheckerMarksShortener(t *testing.T) {
	checker := &LinkChecker{}
	report, _ := checker.Check(context.Background(), "https://bit.ly/abc")
	if report.Verdict != LinkSuspicious {
		t.Fatalf("verdict = %q", report.Verdict)
	}
}

func TestLinkCheckerUnknownByDefault(t *testing.T) {
	checker := &LinkChecker{}
	report, _ := checker.Check(context.Background(), "https://example.com/page")
	if report.Verdict != LinkUnknown {
		t.Fatalf("verdict = %q", report.Verdict)
	}
}

type fakeRuleStore struct {
	rule  HashRule
	found bool
}

func (s *fakeRuleStore) Lookup(context.Context, string) (HashRule, bool, error) {
	return s.rule, s.found, nil
}

func TestScannerUsesTrustedHashRule(t *testing.T) {
	scanner := &Scanner{
		rules: &fakeRuleStore{found: true, rule: HashRule{Verdict: HashRuleTrusted, Reason: "known good build"}},
		clam:  fakeClam{result: ClamResult{Detected: true}},
		now:   time.Now,
	}

	report, err := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "file.jar"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanAllowlisted {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanAllowlisted)
	}
}

func TestScannerUsesMaliciousHashRule(t *testing.T) {
	scanner := &Scanner{
		rules: &fakeRuleStore{found: true, rule: HashRule{Verdict: HashRuleMalicious, Reason: "known malware"}},
		clam:  fakeClam{},
		now:   time.Now,
	}

	report, err := scanner.Scan(context.Background(), []byte("file"), ScanMeta{FileName: "file.jar"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != ScanBlocked {
		t.Fatalf("verdict = %q, want %q", report.Verdict, ScanBlocked)
	}
}

func TestNormalizeHashRule(t *testing.T) {
	rule, err := normalizeHashRule(HashRule{
		SHA256:  strings.Repeat("A", 64),
		Verdict: " MALICIOUS ",
		Reason:  " known bad ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.SHA256 != strings.Repeat("a", 64) || rule.Verdict != HashRuleMalicious || rule.Reason != "known bad" {
		t.Fatalf("unexpected rule: %+v", rule)
	}
}

func TestNormalizeHashRuleRejectsInvalidHash(t *testing.T) {
	_, err := normalizeHashRule(HashRule{SHA256: "abc", Verdict: HashRuleTrusted})
	if err == nil {
		t.Fatal("expected invalid hash error")
	}
}

func TestNormalizeHashRuleRejectsInvalidVerdict(t *testing.T) {
	_, err := normalizeHashRule(HashRule{SHA256: strings.Repeat("a", 64), Verdict: "clean"})
	if err == nil {
		t.Fatal("expected invalid verdict error")
	}
}
