package trust

import (
	"strings"
	"testing"
)

func sampleRelease() Release {
	return Release{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Channel:         "stable",
		Version:         "0.7.5-alpha",
		VersionCode:     705,
		PackageName:     "dev.rdpctd.streamhata.android",
		SHA256:          strings.Repeat("a", 64),
		SignatureSHA256: strings.Repeat("b", 64),
		SignatureType:   "apk-cert",
	}
}

func TestEvaluateVerified(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.5-alpha",
		VersionCode:     705,
		PackageName:     "dev.rdpctd.streamhata.android",
		SHA256:          release.SHA256,
		SignatureSHA256: release.SignatureSHA256,
	})
	if result.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictVerified)
	}
}

func TestEvaluateTamperedHash(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.5-alpha",
		SHA256:          strings.Repeat("c", 64),
		SignatureSHA256: release.SignatureSHA256,
	})
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestEvaluateTamperedSignature(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.5-alpha",
		SHA256:          release.SHA256,
		SignatureSHA256: strings.Repeat("d", 64),
	})
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestEvaluateTamperedVersion(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.6-alpha",
		SHA256:          release.SHA256,
		SignatureSHA256: release.SignatureSHA256,
	})
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestEvaluateSuspiciousWithoutReleaseSignature(t *testing.T) {
	release := sampleRelease()
	release.SignatureSHA256 = ""
	result := evaluate(release, VerifyInput{
		ProductSlug: "streamhata",
		Platform:    "android",
		Version:     "0.7.5-alpha",
		SHA256:      release.SHA256,
	})
	if result.Verdict != VerdictSuspicious {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictSuspicious)
	}
}

func TestEvaluateSuspiciousWhenSignatureMissing(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug: "streamhata",
		Platform:    "android",
		Version:     "0.7.5-alpha",
		SHA256:      release.SHA256,
	})
	if result.Verdict != VerdictSuspicious {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictSuspicious)
	}
}

func TestEvaluateTamperedVersionCode(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.5-alpha",
		VersionCode:     999,
		SHA256:          release.SHA256,
		SignatureSHA256: release.SignatureSHA256,
	})
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestEvaluateTamperedPackage(t *testing.T) {
	release := sampleRelease()
	result := evaluate(release, VerifyInput{
		ProductSlug:     "streamhata",
		Platform:        "android",
		Version:         "0.7.5-alpha",
		PackageName:     "com.example.fake",
		SHA256:          release.SHA256,
		SignatureSHA256: release.SignatureSHA256,
	})
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestNormalizeRelease(t *testing.T) {
	release, err := normalizeRelease(Release{
		ProductSlug: " StreamHata ",
		Platform:    " ANDROID ",
		Version:     " 0.7.5-alpha ",
		SHA256:      strings.Repeat("A", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.ProductSlug != "StreamHata" || release.Platform != "android" || release.Channel != "stable" {
		t.Fatalf("unexpected normalized release: %+v", release)
	}
	if release.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("sha256 was not normalized: %q", release.SHA256)
	}
}

func TestNormalizeReleaseRejectsInvalidHash(t *testing.T) {
	_, err := normalizeRelease(Release{
		ProductSlug: "streamhata",
		Platform:    "android",
		Version:     "0.7.5-alpha",
		SHA256:      "abc",
	})
	if err == nil {
		t.Fatal("expected invalid hash error")
	}
}
