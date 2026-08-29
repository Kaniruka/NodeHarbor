package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// IPLarkProvider is a replaceable, best-effort adapter. It returns only the
// provider-neutral IP Score and hides HTML/JSON response details from the
// evaluation domain. Its HTTP client may be configured with a Test Channel's
// transport by the platform assembly.
type IPLarkProvider struct {
	Client    *http.Client
	Endpoint  string
	UserAgent string
	Timeout   time.Duration
}

func NewIPLarkProvider(client *http.Client) IPLarkProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return IPLarkProvider{Client: client, Endpoint: "https://iplark.com/ipscore", UserAgent: "NodeHarbor/1.0", Timeout: 10 * time.Second}
}

func (provider IPLarkProvider) Name() string { return "iplark" }

func (provider IPLarkProvider) Score(ctx context.Context, exitIdentity string) (float64, error) {
	return provider.score(ctx, exitIdentity, provider.Client)
}

func (provider IPLarkProvider) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	return provider.score(ctx, exitIdentity, client)
}

func (provider IPLarkProvider) score(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if client == nil || provider.Endpoint == "" {
		return 0, providerUnavailable("IPLark provider is not configured")
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	parsed, err := url.Parse(provider.Endpoint)
	if err != nil {
		return 0, providerUnavailable("IPLark provider endpoint is invalid")
	}
	query := parsed.Query()
	query.Set("ip", exitIdentity)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, providerUnavailableWithCause("create IPLark request", err)
	}
	request.Header.Set("Accept", "application/json, text/html")
	if provider.UserAgent != "" {
		request.Header.Set("User-Agent", provider.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, providerUnavailableWithCause("IPLark request failed", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return 0, providerUnavailableWithCause("IPLark response could not be read", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, providerUnavailable(fmt.Sprintf("IPLark provider unavailable: HTTP %d", response.StatusCode))
	}
	if score, ok := parseIPLarkJSON(body); ok {
		return score, nil
	}
	if score, ok := parseIPLarkHTML(body); ok {
		return score, nil
	}
	return 0, providerUnavailable("IPLark provider unavailable: score was not found")
}

func parseIPLarkJSON(body []byte) (float64, bool) {
	var value any
	if json.Unmarshal(body, &value) != nil || containsFailureMarker(value) {
		return 0, false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return 0, false
	}
	status, ok := stringField(envelope, "status")
	if !ok || strings.ToLower(status) != "success" {
		return 0, false
	}
	var data map[string]json.RawMessage
	if raw, ok := envelope["data"]; ok {
		if json.Unmarshal(raw, &data) != nil {
			return 0, false
		}
		var dataValue any
		if json.Unmarshal(raw, &dataValue) != nil || containsFailureMarker(dataValue) {
			return 0, false
		}
		return directScore(data)
	}
	if score, ok := directScore(envelope); ok {
		return score, true
	}
	return 0, false
}

func containsFailureMarker(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if strings.Contains(normalized, "error") || strings.Contains(normalized, "captcha") || strings.Contains(normalized, "challenge") {
				return true
			}
			if containsFailureMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsFailureMarker(child) {
				return true
			}
		}
	case string:
		content := strings.ToLower(item)
		for _, marker := range []string{"captcha", "challenge", "checking your browser", "just a moment", "verify you are human", "access denied"} {
			if strings.Contains(content, marker) {
				return true
			}
		}
	}
	return false
}

func directScore(fields map[string]json.RawMessage) (float64, bool) {
	for key, raw := range fields {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		if normalized != "score" && normalized != "ipscore" {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) == nil {
			if score, ok := number(value); ok && score >= 0 && score <= 100 {
				return score, true
			}
		}
	}
	return 0, false
}

func stringField(fields map[string]json.RawMessage, key string) (string, bool) {
	value, ok := fields[key]
	if !ok {
		return "", false
	}
	var result string
	if json.Unmarshal(value, &result) != nil {
		return "", false
	}
	return result, true
}

func number(value any) (float64, bool) {
	switch item := value.(type) {
	case float64:
		return item, true
	case json.Number:
		score, err := item.Float64()
		return score, err == nil
	case string:
		score, err := strconv.ParseFloat(strings.TrimSpace(item), 64)
		return score, err == nil
	}
	return 0, false
}

var iplarkHTMLScore = regexp.MustCompile(`(?is)^\s*<(?:div|span|p)[^>]*>\s*ip\s*score\s*<(?:strong|b|span)[^>]*>\s*([0-9]{1,3}(?:\.[0-9]+)?)\s*</(?:strong|b|span)>\s*</(?:div|span|p)>\s*$`)

func parseIPLarkHTML(body []byte) (float64, bool) {
	content := strings.ToLower(string(body))
	for _, marker := range []string{"captcha", "challenge", "checking your browser", "just a moment", "verify you are human", "access denied", "forbidden", "blocked", "error"} {
		if strings.Contains(content, marker) {
			return 0, false
		}
	}
	match := iplarkHTMLScore.FindSubmatch(body)
	if len(match) != 2 {
		return 0, false
	}
	score, err := strconv.ParseFloat(string(match[1]), 64)
	return score, err == nil && score >= 0 && score <= 100
}
