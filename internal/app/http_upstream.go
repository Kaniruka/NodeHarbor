package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	document, _, err := upstream.FetchWithMetadata(ctx, request)
	return document, err
}

func (upstream HTTPUpstream) FetchWithMetadata(ctx context.Context, request UpstreamRequest) ([]byte, UpstreamMetadata, error) {
	location, err := url.Parse(request.Location)
	if err != nil || (location.Scheme != "http" && location.Scheme != "https") || location.Host == "" {
		return nil, UpstreamMetadata{}, errors.New("Upstream Subscription URL must use http or https")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.Location, nil)
	if err != nil {
		return nil, UpstreamMetadata{}, fmt.Errorf("create Upstream Subscription request: %w", err)
	}
	if request.UserAgent != "" {
		httpRequest.Header.Set("User-Agent", request.UserAgent)
	} else {
		httpRequest.Header.Set("User-Agent", "mihomo/"+MihomoVersion)
	}
	response, err := upstream.client.Do(httpRequest)
	if err != nil {
		return nil, UpstreamMetadata{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, UpstreamMetadata{}, fmt.Errorf("Upstream Subscription returned HTTP %d", response.StatusCode)
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, maximumUpstreamDocumentBytes+1))
	if err != nil {
		return nil, UpstreamMetadata{}, err
	}
	if len(document) > maximumUpstreamDocumentBytes {
		return nil, UpstreamMetadata{}, errors.New("Upstream Subscription exceeds 10 MiB")
	}
	return decodeBase64SubscriptionDocument(document), parseUpstreamMetadata(response.Header.Get("subscription-userinfo")), nil
}

func decodeBase64SubscriptionDocument(document []byte) []byte {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(document, []byte{0xef, 0xbb, 0xbf}))
	if bytes.Contains(bytes.ToLower(trimmed), []byte("proxies:")) {
		return document
	}
	compact := strings.Join(strings.Fields(string(trimmed)), "")
	if compact == "" {
		return document
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(compact)
		if err != nil || len(bytes.TrimSpace(decoded)) == 0 {
			continue
		}
		if converted, ok := proxyURIListToYAML(decoded); ok {
			return converted
		}
		return decoded
	}
	return document
}

func parseUpstreamMetadata(header string) UpstreamMetadata {
	var metadata UpstreamMetadata
	for _, part := range strings.Split(header, ";") {
		pieces := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pieces) != 2 {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(pieces[1]), 10, 64)
		if err != nil || value < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(pieces[0])) {
		case "upload":
			metadata.UploadBytes = value
		case "download":
			metadata.DownloadBytes = value
		case "total":
			metadata.TotalBytes = value
		case "expire":
			metadata.ExpiresAt = time.Unix(value, 0).UTC().Format(time.RFC3339)
		}
	}
	return metadata
}
