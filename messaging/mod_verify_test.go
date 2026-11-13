package messaging

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/mods"
)

type fakeModVerifier struct {
	result          mods.Result
	err             error
	gotSHA512       string
	gotFingerprints int
	calls           int
}

func (f *fakeModVerifier) Verify(_ context.Context, sha512 string, fingerprints []uint32) (mods.Result, error) {
	f.calls++
	f.gotSHA512 = sha512
	f.gotFingerprints = len(fingerprints)
	return f.result, f.err
}

func TestVerifyModVerified(t *testing.T) {
	data := []byte("mod bytes")
	sum := sha512.Sum512(data)
	verifier := &fakeModVerifier{result: mods.Result{Verdict: mods.VerdictVerified, FileName: "mod.jar"}}

	result, err := verifyMod(context.Background(), verifier, "mod.jar", data)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Verdict != mods.VerdictVerified {
		t.Fatalf("unexpected result: %+v", result)
	}
	if verifier.gotSHA512 != hex.EncodeToString(sum[:]) {
		t.Fatal("sha512 was not passed to verifier")
	}
	if verifier.gotFingerprints == 0 {
		t.Fatal("fingerprints were not passed to verifier")
	}
}

func TestVerifyModTampered(t *testing.T) {
	verifier := &fakeModVerifier{result: mods.Result{Verdict: mods.VerdictTampered}}

	_, err := verifyMod(context.Background(), verifier, "MOD.JAR", []byte("mod bytes"))
	if !errors.Is(err, ErrModTampered) {
		t.Fatalf("error = %v, want ErrModTampered", err)
	}
}

func TestVerifyModUnavailable(t *testing.T) {
	verifier := &fakeModVerifier{err: errors.New("source offline")}

	_, err := verifyMod(context.Background(), verifier, "mod.jar", []byte("mod bytes"))
	if err == nil || errors.Is(err, ErrModTampered) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyModSkipsNonJar(t *testing.T) {
	verifier := &fakeModVerifier{}
	result, err := verifyMod(context.Background(), verifier, "image.png", []byte("png"))
	if err != nil || result != nil {
		t.Fatalf("unexpected result: %+v %v", result, err)
	}
	if verifier.calls != 0 {
		t.Fatal("verifier must not be called for non-jar files")
	}
}
