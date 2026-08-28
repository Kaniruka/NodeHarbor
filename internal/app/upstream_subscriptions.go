package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maximumUpstreamSubscriptions = 10

type upstreamKind string

const (
	upstreamKindURL    upstreamKind = "url"
	upstreamKindUpload upstreamKind = "upload"
	upstreamKindPaste  upstreamKind = "paste"
)

type upstreamRefreshStatus string

const (
	upstreamRefreshPending upstreamRefreshStatus = "pending"
	upstreamRefreshSuccess upstreamRefreshStatus = "success"
	upstreamRefreshStale   upstreamRefreshStatus = "stale"
)

type upstreamSubscription struct {
	ID                     string                `json:"id"`
	Name                   string                `json:"name"`
	Kind                   upstreamKind          `json:"kind"`
	URL                    string                `json:"url,omitempty"`
	UserAgent              string                `json:"userAgent,omitempty"`
	ConfiguredDocument     string                `json:"configuredDocument,omitempty"`
	LastSuccessfulDocument string                `json:"lastSuccessfulDocument,omitempty"`
	ProxyNodeCount         int                   `json:"proxyNodeCount"`
	Enabled                bool                  `json:"enabled"`
	RefreshStatus          upstreamRefreshStatus `json:"refreshStatus"`
	LastError              string                `json:"lastError,omitempty"`
	LastSuccessAt          string                `json:"lastSuccessAt,omitempty"`
	CreatedAt              string                `json:"createdAt"`
	UpdatedAt              string                `json:"updatedAt"`
}

type createUpstreamSubscriptionRequest struct {
	Name      string       `json:"name"`
	Kind      upstreamKind `json:"kind"`
	URL       string       `json:"url"`
	UserAgent string       `json:"userAgent"`
	Document  string       `json:"document"`
}

type updateUpstreamSubscriptionRequest struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	UserAgent string `json:"userAgent"`
	Document  string `json:"document"`
}

func (application *Application) initializeUpstreamSubscriptions(ctx context.Context) error {
	statements := []string{`CREATE TABLE IF NOT EXISTS upstream_subscriptions (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		url TEXT NOT NULL DEFAULT '',
		user_agent TEXT NOT NULL DEFAULT '',
		configured_document BLOB NOT NULL DEFAULT '',
		last_successful_document BLOB NOT NULL DEFAULT '',
		proxy_node_count INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1,
		refresh_status TEXT NOT NULL DEFAULT 'pending',
		last_error TEXT NOT NULL DEFAULT '',
		last_success_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`, `CREATE TRIGGER IF NOT EXISTS enforce_upstream_subscription_limit
		BEFORE INSERT ON upstream_subscriptions
		WHEN (SELECT COUNT(*) FROM upstream_subscriptions) >= 10
		BEGIN
			SELECT RAISE(ABORT, 'at most 10 Upstream Subscriptions are allowed');
		END`}
	for _, statement := range statements {
		if _, err := application.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize Upstream Subscription storage: %w", err)
		}
	}
	return nil
}

func (application *Application) registerUpstreamSubscriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/upstream-subscriptions", application.handleListUpstreamSubscriptions)
	mux.HandleFunc("POST /api/upstream-subscriptions", application.handleCreateUpstreamSubscription)
	mux.HandleFunc("PUT /api/upstream-subscriptions/{id}", application.handleUpdateUpstreamSubscription)
	mux.HandleFunc("PATCH /api/upstream-subscriptions/{id}", application.handleSetUpstreamSubscriptionEnabled)
	mux.HandleFunc("DELETE /api/upstream-subscriptions/{id}", application.handleDeleteUpstreamSubscription)
	mux.HandleFunc("POST /api/upstream-subscriptions/{id}/refresh", application.handleRefreshUpstreamSubscription)
}

func (application *Application) handleListUpstreamSubscriptions(response http.ResponseWriter, request *http.Request) {
	subscriptions, err := application.listUpstreamSubscriptions(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, subscriptions)
}

func (application *Application) handleCreateUpstreamSubscription(response http.ResponseWriter, request *http.Request) {
	input, err := decodeCreateUpstreamSubscriptionRequest(response, request)
	if err != nil {
		writeError(response, http.StatusBadRequest, errors.New("invalid Upstream Subscription request"))
		return
	}
	if input.Name == "" {
		writeError(response, http.StatusBadRequest, errors.New("Upstream Subscription name is required"))
		return
	}
	if input.Kind != upstreamKindURL && input.Kind != upstreamKindPaste && input.Kind != upstreamKindUpload {
		writeError(response, http.StatusBadRequest, errors.New("Upstream Subscription kind must be url, upload, or paste"))
		return
	}
	var count int
	if err := application.database.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM upstream_subscriptions`).Scan(&count); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if count >= maximumUpstreamSubscriptions {
		writeError(response, http.StatusConflict, errors.New("at most 10 Upstream Subscriptions are allowed"))
		return
	}
	var document []byte
	var configuredDocument string
	var acquisitionError error
	if input.Kind == upstreamKindURL {
		if input.URL == "" {
			writeError(response, http.StatusBadRequest, errors.New("URL Upstream Subscription requires url"))
			return
		}
		document, acquisitionError = application.dependencies.Upstream.Fetch(request.Context(), UpstreamRequest{Location: input.URL, UserAgent: input.UserAgent})
		if acquisitionError != nil {
			writeError(response, http.StatusBadGateway, fmt.Errorf("fetch Upstream Subscription: %w", acquisitionError))
			return
		}
	} else {
		if input.Document == "" {
			writeError(response, http.StatusBadRequest, errors.New("uploaded or pasted Upstream Subscription requires document"))
			return
		}
		configuredDocument = input.Document
		document = []byte(input.Document)
	}
	proxyNodeCount, err := application.validateUpstreamDocument(request.Context(), document)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, err)
		return
	}
	id, err := randomID()
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = application.database.ExecContext(request.Context(), `INSERT INTO upstream_subscriptions(
		id, name, kind, url, user_agent, configured_document, last_successful_document, proxy_node_count, enabled,
		refresh_status, last_success_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`, id, input.Name, input.Kind, input.URL, input.UserAgent, configuredDocument, document, proxyNodeCount, upstreamRefreshSuccess, now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "at most 10 Upstream Subscriptions") {
			writeError(response, http.StatusConflict, errors.New("at most 10 Upstream Subscriptions are allowed"))
			return
		}
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	subscription, err := application.getUpstreamSubscription(request.Context(), id)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusCreated, subscription)
}

func (application *Application) handleRefreshUpstreamSubscription(response http.ResponseWriter, request *http.Request) {
	subscription, err := application.getUpstreamSubscription(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, err)
		return
	}
	updated, status, err := application.synchronizeUpstreamSubscription(request.Context(), subscription)
	if err != nil {
		writeError(response, status, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (application *Application) handleUpdateUpstreamSubscription(response http.ResponseWriter, request *http.Request) {
	subscription, err := application.getUpstreamSubscription(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusNotFound, err)
		return
	}
	input, err := decodeUpdateUpstreamSubscriptionRequest(response, request)
	if err != nil || input.Name == "" {
		writeError(response, http.StatusBadRequest, errors.New("Upstream Subscription name is required"))
		return
	}
	subscription.Name = input.Name
	if subscription.Kind == upstreamKindURL {
		if input.URL == "" {
			writeError(response, http.StatusBadRequest, errors.New("URL Upstream Subscription requires url"))
			return
		}
		subscription.URL = input.URL
		subscription.UserAgent = input.UserAgent
	} else {
		if input.Document == "" {
			writeError(response, http.StatusBadRequest, errors.New("uploaded or pasted Upstream Subscription requires document"))
			return
		}
		subscription.ConfiguredDocument = input.Document
	}
	updated, status, err := application.synchronizeUpstreamSubscription(request.Context(), subscription)
	if err != nil {
		writeError(response, status, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (application *Application) handleSetUpstreamSubscriptionEnabled(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Enabled == nil {
		writeError(response, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	result, err := application.database.ExecContext(request.Context(), `UPDATE upstream_subscriptions SET enabled = ?, updated_at = ? WHERE id = ?`,
		*input.Enabled, time.Now().UTC().Format(time.RFC3339Nano), request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		writeError(response, http.StatusNotFound, errors.New("Upstream Subscription not found"))
		return
	}
	subscription, err := application.getUpstreamSubscription(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, subscription)
}

func (application *Application) handleDeleteUpstreamSubscription(response http.ResponseWriter, request *http.Request) {
	result, err := application.database.ExecContext(request.Context(), `DELETE FROM upstream_subscriptions WHERE id = ?`, request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		writeError(response, http.StatusNotFound, errors.New("Upstream Subscription not found"))
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Application) acquireUpstreamDocument(ctx context.Context, subscription upstreamSubscription) ([]byte, int, error) {
	if subscription.Kind == upstreamKindURL {
		document, err := application.dependencies.Upstream.Fetch(ctx, UpstreamRequest{Location: subscription.URL, UserAgent: subscription.UserAgent})
		if err != nil {
			return nil, http.StatusBadGateway, fmt.Errorf("fetch Upstream Subscription: %w", err)
		}
		return document, http.StatusOK, nil
	}
	return []byte(subscription.ConfiguredDocument), http.StatusOK, nil
}

func (application *Application) synchronizeUpstreamSubscription(ctx context.Context, subscription upstreamSubscription) (upstreamSubscription, int, error) {
	document, status, err := application.acquireUpstreamDocument(ctx, subscription)
	if err != nil {
		return application.recordUpstreamRefreshFailure(ctx, subscription, status, err)
	}
	proxyNodeCount, err := application.validateUpstreamDocument(ctx, document)
	if err != nil {
		return application.recordUpstreamRefreshFailure(ctx, subscription, http.StatusUnprocessableEntity, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = application.database.ExecContext(ctx, `UPDATE upstream_subscriptions SET
		name = ?, url = ?, user_agent = ?, configured_document = ?, last_successful_document = ?,
		proxy_node_count = ?, refresh_status = ?, last_error = '', last_success_at = ?, updated_at = ?
		WHERE id = ?`, subscription.Name, subscription.URL, subscription.UserAgent, subscription.ConfiguredDocument,
		document, proxyNodeCount, upstreamRefreshSuccess, now, now, subscription.ID)
	if err != nil {
		return upstreamSubscription{}, http.StatusInternalServerError, err
	}
	updated, err := application.getUpstreamSubscription(ctx, subscription.ID)
	if err != nil {
		return upstreamSubscription{}, http.StatusInternalServerError, err
	}
	return updated, http.StatusOK, nil
}

func (application *Application) recordUpstreamRefreshFailure(ctx context.Context, subscription upstreamSubscription, status int, refreshError error) (upstreamSubscription, int, error) {
	_, err := application.database.ExecContext(ctx, `UPDATE upstream_subscriptions SET
		name = ?, url = ?, user_agent = ?, configured_document = ?, refresh_status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		subscription.Name, subscription.URL, subscription.UserAgent, subscription.ConfiguredDocument, upstreamRefreshStale,
		refreshError.Error(), time.Now().UTC().Format(time.RFC3339Nano), subscription.ID)
	if err != nil {
		return upstreamSubscription{}, http.StatusInternalServerError, err
	}
	return upstreamSubscription{}, status, refreshError
}

func decodeUpdateUpstreamSubscriptionRequest(response http.ResponseWriter, request *http.Request) (updateUpstreamSubscriptionRequest, error) {
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		name, document, err := decodeUploadedUpstreamDocument(response, request)
		return updateUpstreamSubscriptionRequest{Name: name, Document: document}, err
	}
	var input updateUpstreamSubscriptionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 10<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	return input, nil
}

func decodeUploadedUpstreamDocument(response http.ResponseWriter, request *http.Request) (string, string, error) {
	request.Body = http.MaxBytesReader(response, request.Body, 10<<20)
	if err := request.ParseMultipartForm(10 << 20); err != nil {
		return "", "", err
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	file, _, err := request.FormFile("file")
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
	if err != nil || len(document) > 10<<20 {
		return "", "", errors.New("uploaded Upstream Subscription is too large")
	}
	return request.FormValue("name"), string(document), nil
}

func decodeCreateUpstreamSubscriptionRequest(response http.ResponseWriter, request *http.Request) (createUpstreamSubscriptionRequest, error) {
	var input createUpstreamSubscriptionRequest
	if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data") {
		name, document, err := decodeUploadedUpstreamDocument(response, request)
		return createUpstreamSubscriptionRequest{Name: name, Kind: upstreamKindUpload, Document: document}, err
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 10<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	return input, nil
}

func (application *Application) validateUpstreamDocument(ctx context.Context, document []byte) (int, error) {
	var parsed struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(document, &parsed); err != nil {
		return 0, fmt.Errorf("invalid Clash/Mihomo YAML: %w", err)
	}
	if len(parsed.Proxies) == 0 {
		return 0, errors.New("Upstream Subscription contains no Proxy Nodes")
	}
	if err := application.dependencies.Kernel.Validate(ctx, document); err != nil {
		return 0, fmt.Errorf("Mihomo rejected Upstream Subscription: %w", err)
	}
	return len(parsed.Proxies), nil
}

func (application *Application) listUpstreamSubscriptions(ctx context.Context) ([]upstreamSubscription, error) {
	rows, err := application.database.QueryContext(ctx, `SELECT id, name, kind, url, user_agent, configured_document,
		last_successful_document, proxy_node_count, enabled, refresh_status, last_error, last_success_at, created_at, updated_at
		FROM upstream_subscriptions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]upstreamSubscription, 0)
	for rows.Next() {
		subscription, err := scanUpstreamSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, subscription)
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanUpstreamSubscription(row rowScanner) (upstreamSubscription, error) {
	var result upstreamSubscription
	var enabled int
	err := row.Scan(&result.ID, &result.Name, &result.Kind, &result.URL, &result.UserAgent, &result.ConfiguredDocument,
		&result.LastSuccessfulDocument, &result.ProxyNodeCount, &enabled, &result.RefreshStatus, &result.LastError,
		&result.LastSuccessAt, &result.CreatedAt, &result.UpdatedAt)
	result.Enabled = enabled != 0
	return result, err
}

func (application *Application) getUpstreamSubscription(ctx context.Context, id string) (upstreamSubscription, error) {
	row := application.database.QueryRowContext(ctx, `SELECT id, name, kind, url, user_agent, configured_document,
		last_successful_document, proxy_node_count, enabled, refresh_status, last_error, last_success_at, created_at, updated_at
		FROM upstream_subscriptions WHERE id = ?`, id)
	result, err := scanUpstreamSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return result, errors.New("Upstream Subscription not found")
	}
	return result, err
}
