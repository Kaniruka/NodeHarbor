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

// IPCheckProvider uses the same provider-neutral contract as IPLark. The
// endpoint remains configurable because the public site is an unstable web
// integration rather than a guaranteed API.
type IPCheckProvider struct {
	Client    *http.Client
	Endpoint  string
	UserAgent string
	Timeout   time.Duration
}

func NewIPCheckProvider(client *http.Client) IPCheckProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return IPCheckProvider{Client: client, Endpoint: "https://ipcheck.ing/api/ipscore", UserAgent: "NodeHarbor/1.0", Timeout: 10 * time.Second}
}

func (provider IPCheckProvider) Name() string { return "ipcheck" }
func (provider IPCheckProvider) Score(ctx context.Context, exitIdentity string) (float64, error) {
	return provider.score(ctx, exitIdentity, provider.Client)
}
func (provider IPCheckProvider) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	return provider.score(ctx, exitIdentity, client)
}
func (provider IPCheckProvider) score(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if client == nil || provider.Endpoint == "" {
		return 0, providerUnavailable("IPCheck.ing provider is not configured")
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoint, err := url.Parse(provider.Endpoint)
	if err != nil {
		return 0, providerUnavailable("IPCheck.ing provider endpoint is invalid")
	}
	query := endpoint.Query()
	query.Set("ip", exitIdentity)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, providerUnavailable("IPCheck.ing request could not be created")
	}
	request.Header.Set("Accept", "application/json, text/html")
	if provider.UserAgent != "" {
		request.Header.Set("User-Agent", provider.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, providerUnavailableWithCause("IPCheck.ing request failed", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if readErr != nil {
		return 0, providerUnavailable("IPCheck.ing response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, providerUnavailable("IPCheck.ing provider unavailable")
	}
	if score, ok := parseIPCheckJSON(body); ok {
		return score, nil
	}
	if score, ok := parseIPCheckHTML(body); ok {
		return score, nil
	}
	return 0, providerUnavailable("IPCheck.ing score could not be parsed")
}

func parseIPCheckJSON(body []byte) (float64, bool) {
	var value any
	if json.Unmarshal(body, &value) != nil || containsFailureMarker(value) || containsIPCheckFailureStatus(value) {
		return 0, false
	}
	return findIPCheckScore(value)
}

func containsIPCheckFailureStatus(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if normalized == "status" {
				if status, ok := child.(string); ok {
					switch strings.ToLower(strings.TrimSpace(status)) {
					case "", "success", "ok", "available", "complete", "completed":
					default:
						return true
					}
				}
			}
			if containsIPCheckFailureStatus(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsIPCheckFailureStatus(child) {
				return true
			}
		}
	}
	return false
}

func findIPCheckScore(value any) (float64, bool) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if normalized == "score" || normalized == "ipscore" {
				if score, ok := number(child); ok && score >= 0 && score <= 100 {
					return score, true
				}
			}
		}
		for _, child := range item {
			if score, ok := findIPCheckScore(child); ok {
				return score, true
			}
		}
	case []any:
		for _, child := range item {
			if score, ok := findIPCheckScore(child); ok {
				return score, true
			}
		}
	}
	return 0, false
}

var ipCheckHTMLScore = regexp.MustCompile(`(?is)(?:ip\s*score|score)\s*(?:</?[^>]+>\s*)*[:=]?\s*(?:</?[^>]+>\s*)*([0-9]{1,3}(?:\.[0-9]+)?)`)

func parseIPCheckHTML(body []byte) (float64, bool) {
	content := strings.ToLower(string(body))
	for _, marker := range []string{"captcha", "challenge", "checking your browser", "just a moment", "verify you are human", "access denied", "forbidden", "blocked", "error"} {
		if strings.Contains(content, marker) {
			return 0, false
		}
	}
	match := ipCheckHTMLScore.FindSubmatch(body)
	if len(match) != 2 {
		return 0, false
	}
	score, err := strconv.ParseFloat(string(match[1]), 64)
	return score, err == nil && score >= 0 && score <= 100
}

type providerUnavailableError struct{ message string }

func (err providerUnavailableError) Error() string { return err.message }
func (err providerUnavailableError) Unwrap() error { return errScoringProviderUnavailable }
func providerUnavailable(message string) error     { return providerUnavailableError{message: message} }
func providerUnavailableWithCause(message string, cause error) error {
	if cause == nil {
		return providerUnavailable(message)
	}
	return fmt.Errorf("%w: %w", providerUnavailable(message), cause)
}
