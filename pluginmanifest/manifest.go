package pluginmanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const (
	CurrentSchemaVersion = 1
	MaxSize              = 64 << 10
	KindPlugin           = "plugin"
	KindMod              = "mod"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	apiPattern     = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)
	shaPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	allowedPerms   = map[string]bool{
		"network":          true,
		"filesystem.read":  true,
		"filesystem.write": true,
		"clipboard.read":   true,
		"clipboard.write":  true,
		"notifications":    true,
		"obs":              true,
		"twitch.chat":      true,
		"stream.state":     true,
		"commands":         true,
		"ui.panel":         true,
		"storage":          true,
	}
)

type Compatibility struct {
	GameVersions []string `json:"gameVersions,omitempty"`
	Loaders      []string `json:"loaders,omitempty"`
	Platforms    []string `json:"platforms,omitempty"`
}

type Artifact struct {
	URL           string `json:"url,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Size          int64  `json:"size,omitempty"`
	SignatureType string `json:"signatureType,omitempty"`
	Signature     string `json:"signature,omitempty"`
}

type Publisher struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Trust string `json:"trust,omitempty"`
}

type Manifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	Kind          string        `json:"kind"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Description   string        `json:"description,omitempty"`
	Authors       []string      `json:"authors,omitempty"`
	License       string        `json:"license,omitempty"`
	Homepage      string        `json:"homepage,omitempty"`
	APIVersion    string        `json:"apiVersion,omitempty"`
	Entry         string        `json:"entry,omitempty"`
	Permissions   []string      `json:"permissions,omitempty"`
	Compatibility Compatibility `json:"compatibility,omitempty"`
	Artifact      Artifact      `json:"artifact,omitempty"`
	Publisher     Publisher     `json:"publisher,omitempty"`
}

func Parse(data []byte) (Manifest, error) {
	if len(data) > MaxSize {
		return Manifest{}, fmt.Errorf("manifest is larger than %d bytes", MaxSize)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("manifest must contain a single json object")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}

	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schemaVersion must be %d", CurrentSchemaVersion)
	}
	if m.Kind != KindPlugin && m.Kind != KindMod {
		return errors.New("kind must be plugin or mod")
	}
	if !idPattern.MatchString(m.ID) {
		return errors.New("id must be 3-64 lowercase letters, digits, dots, dashes or underscores")
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is required")
	}
	if !versionPattern.MatchString(m.Version) {
		return errors.New("version must be semver, for example 1.2.3")
	}
	if m.Homepage != "" {
		if err := validateURL(m.Homepage); err != nil {
			return fmt.Errorf("homepage: %w", err)
		}
	}
	if m.Kind == KindPlugin {
		if !apiPattern.MatchString(m.APIVersion) {
			return errors.New("apiVersion is required for plugins")
		}
		if err := validateEntry(m.Entry); err != nil {
			return err
		}
	} else if m.Entry != "" {
		if err := validateEntry(m.Entry); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(m.Permissions))
	for _, permission := range m.Permissions {
		if !allowedPerms[permission] {
			return fmt.Errorf("unknown permission %q", permission)
		}
		if seen[permission] {
			return fmt.Errorf("duplicate permission %q", permission)
		}
		seen[permission] = true
	}
	if err := validateStrings(m.Compatibility.GameVersions, "gameVersions"); err != nil {
		return err
	}
	if err := validateStrings(m.Compatibility.Loaders, "loaders"); err != nil {
		return err
	}
	if err := validateStrings(m.Compatibility.Platforms, "platforms"); err != nil {
		return err
	}
	return m.Artifact.Validate()
}

func (a Artifact) Validate() error {
	if a.URL != "" {
		if err := validateURL(a.URL); err != nil {
			return fmt.Errorf("artifact.url: %w", err)
		}
	}
	if a.SHA256 != "" && !shaPattern.MatchString(strings.ToLower(a.SHA256)) {
		return errors.New("artifact.sha256 must be 64 hex characters")
	}
	if a.Size < 0 {
		return errors.New("artifact.size must not be negative")
	}
	if a.Signature != "" {
		if a.SignatureType != "minisign" && a.SignatureType != "ed25519" {
			return errors.New("artifact.signatureType must be minisign or ed25519")
		}
		if strings.TrimSpace(a.Signature) == "" {
			return errors.New("artifact.signature is required when signatureType is set")
		}
	} else if a.SignatureType != "" {
		return errors.New("artifact.signatureType requires a signature")
	}

	return nil
}

func (a Artifact) SHA256Hex() ([]byte, error) {
	if !shaPattern.MatchString(strings.ToLower(a.SHA256)) {
		return nil, errors.New("artifact.sha256 must be 64 hex characters")
	}

	return hex.DecodeString(strings.ToLower(a.SHA256))
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func validateEntry(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return errors.New("entry is required for plugins")
	}
	if strings.HasPrefix(entry, "/") || strings.Contains(entry, `\`) || path.Clean(entry) != entry || strings.HasPrefix(entry, "..") {
		return errors.New("entry must be a safe relative path")
	}

	return nil
}

func validateURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errors.New("must be an http(s) URL")
	}

	return nil
}

func validateStrings(values []string, field string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not contain empty values", field)
		}
	}

	return nil
}
