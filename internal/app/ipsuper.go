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

// IPSuperProvider reads the public IPSuper aggregate security score from the
// query page observed in the browser, while retaining the Test Channel
// transport supplied by evaluation.
type IPSuperProvider struct {
	Client               *http.Client
	Endpoint             string
	UserAgent            string
	Timeout              time.Duration
	IPv4IdentityEndpoint string
	IPv6IdentityEndpoint string
}

func NewIPSuperProvider(client *http.Client) IPSuperProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return IPSuperProvider{Client: client, Endpoint: "https://ipsuper.com", UserAgent: "NodeHarbor/1.0", Timeout: 10 * time.Second, IPv4IdentityEndpoint: ipv4IdentityEndpoint, IPv6IdentityEndpoint: ipv6IdentityEndpoint}
}

func (provider IPSuperProvider) Name() string { return "ipsuper" }

func (provider IPSuperProvider) Score(ctx context.Context, exitIdentity string) (float64, error) {
	return provider.score(ctx, exitIdentity, provider.Client)
}

func (provider IPSuperProvider) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	return provider.score(ctx, exitIdentity, client)
}

func (provider IPSuperProvider) score(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if client == nil || provider.Endpoint == "" {
		return 0, providerUnavailable("IPSuper provider is not configured")
	}
	endpoint, err := url.Parse(provider.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return 0, providerUnavailable("IPSuper provider endpoint is invalid")
	}
	timeout := provider.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	scoreURL := *endpoint
	query := scoreURL.Query()
	query.Set("ip", exitIdentity)
	scoreURL.RawQuery = query.Encode()
	body, err := provider.get(requestContext, client, scoreURL.String(), "")
	if err != nil {
		return 0, err
	}
	if score, ok := parseIPSuperAggregateScore(body); ok {
		return score, nil
	}
	reason := "IPSuper provider unavailable: aggregate security score was not found"
	if detail := providerResponseDetail(body); detail != "" {
		reason += ": " + detail
	}
	return 0, providerUnavailable(reason)
}

func (provider IPSuperProvider) get(ctx context.Context, client *http.Client, target, referer string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, providerUnavailableWithCause("create IPSuper request", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	if provider.UserAgent != "" {
		request.Header.Set("User-Agent", provider.UserAgent)
	}
	if referer != "" {
		request.Header.Set("Referer", referer)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, providerUnavailableWithCause("IPSuper request failed", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, providerUnavailable("IPSuper response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		reason := fmt.Sprintf("IPSuper provider unavailable: HTTP %d", response.StatusCode)
		if detail := providerResponseDetail(body); detail != "" {
			reason += ": " + detail
		}
		return nil, providerUnavailable(reason)
	}
	if isProviderChallenge(body) {
		return nil, providerUnavailable("IPSuper provider unavailable: challenge response")
	}
	return body, nil
}

var ipSuperAggregateScore = regexp.MustCompile(`(?is)(?:综合\s*安全\s*分|aggregate\s*security\s*score).*?([0-9]{1,3}(?:\.[0-9]+)?)(?:\s|<[^>]*>)*\/\s*100`)

func parseIPSuperAggregateScore(body []byte) (float64, bool) {
	if isProviderChallenge(body) {
		return 0, false
	}
	matches := ipSuperAggregateScore.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return 0, false
	}
	match := matches[len(matches)-1]
	score, err := strconv.ParseFloat(string(match[1]), 64)
	return score, err == nil && score >= 0 && score <= 100
}

func isProviderChallenge(body []byte) bool {
	content := strings.ToLower(string(body))
	for _, marker := range []string{"captcha", "checking your browser", "just a moment", "verify you are human", "access denied", "forbidden", "blocked"} {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func providerResponseDetail(body []byte) string {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return ""
	}
	detail := strings.Join(strings.Fields(findProviderResponseDetail(value)), " ")
	if len(detail) > 240 {
		return detail[:240]
	}
	return detail
}

func findProviderResponseDetail(value any) string {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			if normalized == "error" || normalized == "message" || normalized == "detail" || normalized == "reason" {
				if detail, ok := child.(string); ok && strings.TrimSpace(detail) != "" {
					return detail
				}
			}
		}
		for _, child := range item {
			if detail := findProviderResponseDetail(child); detail != "" {
				return detail
			}
		}
	case []any:
		for _, child := range item {
			if detail := findProviderResponseDetail(child); detail != "" {
				return detail
			}
		}
	}
	return ""
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
