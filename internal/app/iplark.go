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
}

func NewIPLarkProvider(client *http.Client) IPLarkProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return IPLarkProvider{Client: client, Endpoint: "https://iplark.com/ipscore", UserAgent: "NodeHarbor/1.0"}
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
	parsed, err := url.Parse(provider.Endpoint)
	if err != nil {
		return 0, providerUnavailable("IPLark provider endpoint is invalid")
	}
	query := parsed.Query()
	query.Set("ip", exitIdentity)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return 0, providerUnavailable(fmt.Sprintf("create IPLark request: %v", err))
	}
	request.Header.Set("Accept", "application/json, text/html")
	if provider.UserAgent != "" {
		request.Header.Set("User-Agent", provider.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, providerUnavailable(fmt.Sprintf("IPLark request failed: %v", err))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return 0, providerUnavailable("IPLark response could not be read")
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
	if json.Unmarshal(body, &value) != nil {
		return 0, false
	}
	return findScore(value)
}

func findScore(value any) (float64, bool) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if normalized == "score" || normalized == "ipscore" {
				if score, ok := number(child); ok && score >= 0 && score <= 100 {
					return score, true
				}
			}
			if score, ok := findScore(child); ok {
				return score, true
			}
		}
	case []any:
		for _, child := range item {
			if score, ok := findScore(child); ok {
				return score, true
			}
		}
	}
	return 0, false
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

var iplarkHTMLScore = regexp.MustCompile(`(?i)(?:ip\s*score|score)[^0-9]{0,80}([0-9]{1,3}(?:\.[0-9]+)?)`)

func parseIPLarkHTML(body []byte) (float64, bool) {
	match := iplarkHTMLScore.FindSubmatch(body)
	if len(match) != 2 {
		return 0, false
	}
	score, err := strconv.ParseFloat(string(match[1]), 64)
	return score, err == nil && score >= 0 && score <= 100
}
