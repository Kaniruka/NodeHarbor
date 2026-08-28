package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	)`, `CREATE TABLE IF NOT EXISTS proxy_nodes (
		id TEXT PRIMARY KEY,
		subscription_id TEXT NOT NULL REFERENCES upstream_subscriptions(id) ON DELETE CASCADE,
		fingerprint TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		original_name TEXT NOT NULL,
		config BLOB NOT NULL,
		state TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`, `CREATE INDEX IF NOT EXISTS proxy_nodes_subscription_idx ON proxy_nodes(subscription_id, id)`, `CREATE TRIGGER IF NOT EXISTS enforce_upstream_subscription_limit
		BEFORE INSERT ON upstream_subscriptions
		WHEN (SELECT COUNT(*) FROM upstream_subscriptions) >= 10
		BEGIN
			SELECT RAISE(ABORT, 'at most 10 Upstream Subscriptions are allowed');
		END`, `UPDATE upstream_subscriptions SET refresh_status = 'stale' WHERE refresh_status = 'failed'`}
	for _, statement := range statements {
		if _, err := application.database.ExecContext(ctx, statement); err != nil {
			if strings.Contains(statement, "CREATE TABLE IF NOT EXISTS proxy_nodes") && strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("initialize Upstream Subscription storage: %w", err)
		}
	}
	if _, err := application.database.ExecContext(ctx, `ALTER TABLE proxy_nodes ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate Proxy Node identity storage: %w", err)
	}
	if err := application.migrateProxyNodeFingerprints(ctx); err != nil {
		return err
	}
	return nil
}

func (application *Application) migrateProxyNodeFingerprints(ctx context.Context) error {
	tx, err := application.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Proxy Node identity migration: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, subscription_id, config FROM proxy_nodes WHERE fingerprint = ''`)
	if err != nil {
		return fmt.Errorf("read legacy Proxy Node identities: %w", err)
	}
	type legacyNode struct {
		id, subscriptionID string
		config             []byte
	}
	legacy := make([]legacyNode, 0)
	for rows.Next() {
		var node legacyNode
		if err := rows.Scan(&node.id, &node.subscriptionID, &node.config); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy Proxy Node identity: %w", err)
		}
		legacy = append(legacy, node)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy Proxy Node identities: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read legacy Proxy Node identities: %w", err)
	}
	for _, node := range legacy {
		fingerprint, err := normalizedNodeFingerprintFromYAML(node.config)
		if err != nil {
			return fmt.Errorf("fingerprint legacy Proxy Node: %w", err)
		}
		newID := stableProxyNodeID(node.subscriptionID, fingerprint)
		if newID != node.id {
			if _, err := tx.ExecContext(ctx, `SELECT 1 FROM proxy_nodes WHERE id = ?`, newID); err == nil {
				newID += "-legacy-" + node.id
			}
		}
		if newID != node.id {
			if _, err := tx.ExecContext(ctx, `UPDATE evaluation_results SET node_id = ? WHERE node_id = ?`, newID, node.id); err != nil && !strings.Contains(err.Error(), "no such table") {
				return fmt.Errorf("migrate evaluation result identity: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_nodes SET id = ?, fingerprint = ? WHERE id = ?`, newID, fingerprint, node.id); err != nil {
			return fmt.Errorf("migrate Proxy Node identity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Proxy Node identity migration: %w", err)
	}
	return nil
}

func (application *Application) registerUpstreamSubscriptionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/upstream-subscriptions", application.handleListUpstreamSubscriptions)
	mux.HandleFunc("GET /api/upstream-subscriptions/{id}/nodes", application.handleListProxyNodes)
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
	if err := application.replaceProxyNodes(request.Context(), id, document); err != nil {
		writeError(response, http.StatusInternalServerError, fmt.Errorf("store Proxy Node evaluation: %w", err))
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
	tx, err := application.database.BeginTx(request.Context(), nil)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.ExecContext(request.Context(), `DELETE FROM proxy_nodes WHERE subscription_id = ?`, request.PathValue("id")); err != nil {
		_ = tx.Rollback()
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	result, err := tx.ExecContext(request.Context(), `DELETE FROM upstream_subscriptions WHERE id = ?`, request.PathValue("id"))
	if err != nil {
		_ = tx.Rollback()
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		_ = tx.Rollback()
		writeError(response, http.StatusNotFound, errors.New("Upstream Subscription not found"))
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
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
	if err := application.replaceProxyNodes(ctx, subscription.ID, document); err != nil {
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
	if _, supportsNodeValidation := application.dependencies.Kernel.(NodeValidator); !supportsNodeValidation {
		if err := application.dependencies.Kernel.Validate(ctx, document); err != nil {
			return 0, fmt.Errorf("Mihomo rejected Upstream Subscription: %w", err)
		}
	}
	return len(parsed.Proxies), nil
}

type storedProxyNode struct {
	ID             string
	SubscriptionID string
	SourceName     string
	Name           string
	OriginalName   string
	Fingerprint    string
	Config         map[string]any
	State          proxyNodeState
	Reason         string
	Rejection      *nodeRejection
	CreatedAt      string
}

func (application *Application) replaceProxyNodes(ctx context.Context, subscriptionID string, document []byte) error {
	var parsed struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(document, &parsed); err != nil {
		return err
	}
	// The persisted source name supplies the stable display prefix.
	var sourceName string
	if err := application.database.QueryRowContext(ctx, `SELECT name FROM upstream_subscriptions WHERE id = ?`, subscriptionID).Scan(&sourceName); err != nil {
		return err
	}
	seenFingerprints := map[string]bool{}
	nodes := make([]storedProxyNode, 0, len(parsed.Proxies))
	for index, config := range parsed.Proxies {
		original, _ := config["name"].(string)
		if strings.TrimSpace(original) == "" {
			original = fmt.Sprintf("unnamed-%d", index+1)
		}
		fingerprint, err := normalizedNodeFingerprint(config)
		if err != nil {
			return err
		}
		if seenFingerprints[fingerprint] {
			continue
		}
		seenFingerprints[fingerprint] = true
		id := stableProxyNodeID(subscriptionID, fingerprint)
		validationConfig := cloneProxyNodeConfig(config)
		validationName := fmt.Sprintf("[%s] %s", stableSourcePrefix(sourceName), original)
		validationConfig["name"] = validationName
		node := storedProxyNode{ID: id, SubscriptionID: subscriptionID, SourceName: sourceName, OriginalName: original, Fingerprint: fingerprint, Config: config, State: proxyNodeAccepted, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if validator, ok := application.dependencies.Kernel.(NodeValidator); ok {
			if err := validator.ValidateNode(ctx, ProxyNode{Name: validationName, Config: validationConfig}); err != nil {
				node.State = proxyNodeRejected
				node.Reason = structuredNodeReason(err)
				node.Rejection = &nodeRejection{Code: "validation_failed", Message: err.Error()}
			}
		}
		nodes = append(nodes, node)
	}
	tx, err := application.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := application.loadStoredProxyNodes(ctx, tx, subscriptionID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM proxy_nodes WHERE subscription_id = ?`, subscriptionID); err != nil {
		return err
	}
	allNodes := append(existing, nodes...)
	allocatedNames := allocateProxyNodeNames(allNodes)
	for index := range allNodes {
		node := &allNodes[index]
		node.Name = allocatedNames[node.ID]
		node.Config = cloneProxyNodeConfig(node.Config)
		node.Config["name"] = node.Name
		config, err := yaml.Marshal(node.Config)
		if err != nil {
			return err
		}
		if index < len(existing) {
			if _, err = tx.ExecContext(ctx, `UPDATE proxy_nodes SET name = ?, config = ? WHERE id = ?`, node.Name, config, node.ID); err != nil {
				return err
			}
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO proxy_nodes(id, subscription_id, fingerprint, name, original_name, config, state, reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, node.ID, node.SubscriptionID, node.Fingerprint, node.Name, node.OriginalName, config, node.State, node.Reason, node.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (application *Application) loadStoredProxyNodes(ctx context.Context, tx *sql.Tx, excludingSubscriptionID string) ([]storedProxyNode, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.subscription_id, s.name, p.name, p.original_name, p.fingerprint, p.config, p.state, p.reason, p.created_at
		FROM proxy_nodes p JOIN upstream_subscriptions s ON s.id = p.subscription_id WHERE p.subscription_id <> ?`, excludingSubscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]storedProxyNode, 0)
	for rows.Next() {
		var node storedProxyNode
		var config []byte
		if err := rows.Scan(&node.ID, &node.SubscriptionID, &node.SourceName, &node.Name, &node.OriginalName, &node.Fingerprint, &config, &node.State, &node.Reason, &node.CreatedAt); err != nil {
			return nil, err
		}
		if node.Fingerprint == "" {
			var err error
			node.Fingerprint, err = normalizedNodeFingerprintFromYAML(config)
			if err != nil {
				return nil, err
			}
		}
		if err := yaml.Unmarshal(config, &node.Config); err != nil {
			return nil, err
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

func allocateProxyNodeNames(nodes []storedProxyNode) map[string]string {
	groups := map[string][]*storedProxyNode{}
	for index := range nodes {
		node := &nodes[index]
		base := fmt.Sprintf("[%s] %s", stableSourcePrefix(node.SourceName), node.OriginalName)
		groups[base] = append(groups[base], node)
	}
	result := make(map[string]string, len(nodes))
	for base, group := range groups {
		if len(group) == 1 {
			result[group[0].ID] = base
			continue
		}
		sort.Slice(group, func(left, right int) bool {
			if group[left].Fingerprint != group[right].Fingerprint {
				return group[left].Fingerprint < group[right].Fingerprint
			}
			return group[left].SubscriptionID < group[right].SubscriptionID
		})
		for index, node := range group {
			suffix := node.Fingerprint
			if index > 0 && group[index-1].Fingerprint == node.Fingerprint {
				suffix += "-" + node.SubscriptionID
			}
			result[node.ID] = base + " (" + suffix + ")"
		}
	}
	return result
}

func stableProxyNodeID(subscriptionID, fingerprint string) string {
	return subscriptionID + "-" + fingerprint
}

func cloneProxyNodeConfig(config map[string]any) map[string]any {
	clone := make(map[string]any, len(config))
	for key, value := range config {
		clone[key] = value
	}
	return clone
}

func normalizedNodeFingerprint(config map[string]any) (string, error) {
	canonical := cloneProxyNodeConfig(config)
	delete(canonical, "name")
	data, err := yaml.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func normalizedNodeFingerprintFromYAML(config []byte) (string, error) {
	var parsed map[string]any
	if err := yaml.Unmarshal(config, &parsed); err != nil {
		return "", err
	}
	return normalizedNodeFingerprint(parsed)
}

func stableSourcePrefix(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "source"
	}
	return name
}

func structuredNodeReason(err error) string { return "validation_failed: " + err.Error() }

func rejectionFromReason(reason string) *nodeRejection {
	if reason == "" {
		return nil
	}
	const prefix = "validation_failed: "
	if strings.HasPrefix(reason, prefix) {
		return &nodeRejection{Code: "validation_failed", Message: strings.TrimPrefix(reason, prefix)}
	}
	return &nodeRejection{Code: "validation_failed", Message: reason}
}

func (application *Application) handleListProxyNodes(response http.ResponseWriter, request *http.Request) {
	if _, err := application.getUpstreamSubscription(request.Context(), request.PathValue("id")); err != nil {
		writeError(response, http.StatusNotFound, err)
		return
	}
	rows, err := application.database.QueryContext(request.Context(), `SELECT id, fingerprint, name, original_name, config, state, reason FROM proxy_nodes WHERE subscription_id = ? ORDER BY id`, request.PathValue("id"))
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	result := make([]evaluatedProxyNode, 0)
	for rows.Next() {
		var node evaluatedProxyNode
		var config []byte
		if err := rows.Scan(&node.ID, &node.Fingerprint, &node.Name, &node.OriginalName, &config, &node.State, &node.Reason); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		if err := yaml.Unmarshal(config, &node.Config); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
		if node.State == proxyNodeRejected {
			node.Rejection = rejectionFromReason(node.Reason)
		}
		result = append(result, node)
	}
	if err := rows.Err(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
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
