package mods

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	murmurM = uint32(0x5bd1e995)
	murmurR = 24
)

type CurseForge interface {
	Lookup(ctx context.Context, fingerprints []uint32) (Result, bool, error)
}

type HTTPCurseForge struct {
	URL    string
	Client *http.Client
}

type curseForgeResponse struct {
	Data struct {
		ExactMatches []struct {
			ID   uint32 `json:"id"`
			File struct {
				ID           int64    `json:"id"`
				ModID        int64    `json:"modId"`
				FileName     string   `json:"fileName"`
				DisplayName  string   `json:"displayName"`
				DownloadURL  string   `json:"downloadUrl"`
				GameVersions []string `json:"gameVersions"`
			} `json:"file"`
		} `json:"exactMatches"`
	} `json:"data"`
}

func (c *HTTPCurseForge) Lookup(ctx context.Context, fingerprints []uint32) (Result, bool, error) {
	if len(fingerprints) == 0 {
		return Result{}, false, nil
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]any{"fingerprints": fingerprints})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Result{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Result{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, false, fmt.Errorf("curseforge returned %d", response.StatusCode)
	}
	var payload curseForgeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Result{}, false, err
	}
	for _, match := range payload.Data.ExactMatches {
		if match.File.FileName == "" {
			continue
		}
		return Result{
			Source:       "curseforge",
			Verdict:      VerdictVerified,
			Reason:       "файл найден в CurseForge",
			ProjectID:    strconv.FormatInt(match.File.ModID, 10),
			Title:        firstNonEmpty(match.File.DisplayName, match.File.FileName),
			FileName:     match.File.FileName,
			FileURL:      match.File.DownloadURL,
			GameVersions: match.File.GameVersions,
			CheckedAt:    time.Now(),
		}, true, nil
	}
	return Result{}, false, nil
}

func MurmurHash2(data []byte, seed uint32) uint32 {
	hash := seed ^ uint32(len(data))

	for len(data) >= 4 {
		value := binary.LittleEndian.Uint32(data)
		value *= murmurM
		value ^= value >> murmurR
		value *= murmurM
		hash *= murmurM
		hash ^= value
		data = data[4:]
	}

	switch len(data) {
	case 3:
		hash ^= uint32(data[2]) << 16
		fallthrough
	case 2:
		hash ^= uint32(data[1]) << 8
		fallthrough
	case 1:
		hash ^= uint32(data[0])
		hash *= murmurM
	}

	hash ^= hash >> 13
	hash *= murmurM
	hash ^= hash >> 15

	return hash
}

func Fingerprints(data []byte) []uint32 {
	raw := MurmurHash2(data, 1)
	stripped := stripWhitespace(data)
	normalized := MurmurHash2(stripped, 1)
	if raw == normalized {
		return []uint32{raw}
	}
	return []uint32{raw, normalized}
}

func stripWhitespace(data []byte) []byte {
	result := make([]byte, 0, len(data))
	for _, value := range data {
		switch value {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			result = append(result, value)
		}
	}
	return result
}

func FingerprintJSON(fingerprints []uint32) string {
	parts := make([]string, len(fingerprints))
	for index, value := range fingerprints {
		parts[index] = strconv.FormatUint(uint64(value), 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
