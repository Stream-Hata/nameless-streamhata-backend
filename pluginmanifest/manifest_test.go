package pluginmanifest

import (
	"strings"
	"testing"
)

const validPlugin = `{
  "schemaVersion": 1,
  "kind": "plugin",
  "id": "dev.streamhata.obs-tools",
  "name": "OBS Tools",
  "version": "1.0.0",
  "description": "Scene helpers",
  "authors": ["StreamHata"],
  "license": "MIT",
  "apiVersion": "1",
  "entry": "index.js",
  "permissions": ["obs", "notifications", "storage"],
  "compatibility": {"platforms": ["windows", "linux"]},
  "artifact": {
    "url": "https://streamhata.rdpctd.dev/downloads/obs-tools.zip",
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "size": 1024,
    "signatureType": "minisign",
    "signature": "signature"
  }
}`

func TestParseValidPlugin(t *testing.T) {
	manifest, err := Parse([]byte(validPlugin))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if manifest.ID != "dev.streamhata.obs-tools" || manifest.Kind != KindPlugin {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}

func TestPluginRequiresAPIVersionAndEntry(t *testing.T) {
	for _, replacement := range [][2]string{
		{`"apiVersion": "1",`, ""},
		{`"entry": "index.js",`, ""},
	} {
		raw := strings.Replace(validPlugin, replacement[0], replacement[1], 1)
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("expected validation error for %q", replacement[0])
		}
	}
}

func TestRejectsUnsafeValues(t *testing.T) {
	cases := []string{
		strings.Replace(validPlugin, `"dev.streamhata.obs-tools"`, `"Bad ID"`, 1),
		strings.Replace(validPlugin, `"1.0.0"`, `"1.0"`, 1),
		strings.Replace(validPlugin, `"obs", `, `"root", `, 1),
		strings.Replace(validPlugin, `"entry": "index.js"`, `"entry": "../index.js"`, 1),
		strings.Replace(validPlugin, `"minisign"`, `"md5"`, 1),
	}
	for _, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("expected validation error for %s", raw)
		}
	}
}

func TestModAllowsLoaderMetadata(t *testing.T) {
	raw := `{
	  "schemaVersion": 1,
	  "kind": "mod",
	  "id": "dev.streamhata.example-mod",
	  "name": "Example Mod",
	  "version": "1.2.3",
	  "compatibility": {"gameVersions": ["1.21"], "loaders": ["fabric"]},
	  "artifact": {"sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	}`
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatalf("valid mod rejected: %v", err)
	}
}

func TestDigest(t *testing.T) {
	if got := Digest([]byte("test")); got != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatalf("unexpected digest: %s", got)
	}
}
