package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const maximumUpstreamDocumentBytes = 10 << 20

type HTTPUpstream struct {
	client *http.Client
}

func NewHTTPUpstream(timeout time.Duration) HTTPUpstream {
	return HTTPUpstream{client: &http.Client{Timeout: timeout}}
}

func (upstream HTTPUpstream) Fetch(ctx context.Context, request UpstreamRequest) ([]byte, error) {
	location, err := url.Parse(request.Location)
	if err != nil || (location.Scheme != "http" && location.Scheme != "https") || location.Host == "" {
		return nil, errors.New("Upstream Subscription URL must use http or https")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Location, nil)
	if err != nil {
		return nil, fmt.Errorf("create Upstream Subscription request: %w", err)
	}
	if request.UserAgent != "" {
		httpRequest.Header.Set("User-Agent", request.UserAgent)
	} else {
		httpRequest.Header.Set("User-Agent", "mihomo/"+MihomoVersion)
	}
	response, err := upstream.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Upstream Subscription returned HTTP %d", response.StatusCode)
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, maximumUpstreamDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(document) > maximumUpstreamDocumentBytes {
		return nil, errors.New("Upstream Subscription exceeds 10 MiB")
	}
	return document, nil
}
