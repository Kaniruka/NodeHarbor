package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

type evaluationRun struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Total      int    `json:"total"`
	Passed     int    `json:"passed"`
	Failed     int    `json:"failed"`
}
type evaluationNodeResult struct {
	NodeID          string   `json:"nodeId"`
	Name            string   `json:"name"`
	State           string   `json:"state"`
	Attempts        int      `json:"attempts"`
	Successful      int      `json:"successful"`
	MedianLatencyMS float64  `json:"medianLatencyMs"`
	ExitIdentity    string   `json:"exitIdentity,omitempty"`
	AddressFamily   string   `json:"addressFamily,omitempty"`
	IPScore         *float64 `json:"ipScore,omitempty"`
	Reason          string   `json:"reason,omitempty"`
}
type evaluationRunResponse struct {
	evaluationRun
	Results []evaluationNodeResult `json:"results"`
}
type evaluationNode struct {
	ID     string
	Name   string
	Config map[string]any
}

func (application *Application) initializeEvaluationRuns(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS evaluation_runs (id TEXT PRIMARY KEY, status TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', total INTEGER NOT NULL DEFAULT 0, passed INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS evaluation_results (run_id TEXT NOT NULL, node_id TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL, successful INTEGER NOT NULL, median_latency_ms REAL NOT NULL DEFAULT 0, exit_identity TEXT NOT NULL DEFAULT '', address_family TEXT NOT NULL DEFAULT '', ip_score REAL, reason TEXT NOT NULL DEFAULT '', PRIMARY KEY(run_id, node_id))`,
		`CREATE TABLE IF NOT EXISTS score_cache (provider TEXT NOT NULL, exit_identity TEXT NOT NULL, score REAL NOT NULL, address_family TEXT NOT NULL, scored_at TEXT NOT NULL, PRIMARY KEY(provider, exit_identity))`,
	}
	for _, statement := range statements {
		if _, err := application.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize Evaluation Run storage: %w", err)
		}
	}
	// #5 created evaluation_results before scoring fields existed. Keep existing
	// installations readable without requiring a destructive database reset.
	var columns = map[string]bool{}
	rows, err := application.database.QueryContext(ctx, `PRAGMA table_info(evaluation_results)`)
	if err != nil {
		return fmt.Errorf("inspect Evaluation Run schema: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !columns["address_family"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_results ADD COLUMN address_family TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !columns["ip_score"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_results ADD COLUMN ip_score REAL`); err != nil {
			return err
		}
	}
	return nil
}

func (application *Application) registerEvaluationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/evaluation-runs", application.handleStartEvaluationRun)
	mux.HandleFunc("GET /api/evaluation-runs", application.handleListEvaluationRuns)
	mux.HandleFunc("GET /api/evaluation-runs/current", application.handleCurrentEvaluationRun)
	mux.HandleFunc("GET /api/evaluation-runs/{id}", application.handleGetEvaluationRun)
}

func (application *Application) handleListEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := application.database.QueryContext(r.Context(), `SELECT id, status, started_at, finished_at, total, passed, failed FROM evaluation_runs ORDER BY started_at DESC LIMIT 50`)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	result := make([]evaluationRunResponse, 0)
	for rows.Next() {
		var run evaluationRun
		if err := rows.Scan(&run.ID, &run.Status, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed); err != nil {
			writeError(w, 500, err)
			return
		}
		result = append(result, application.runResponse(r.Context(), run))
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, result)
}

func (application *Application) handleStartEvaluationRun(w http.ResponseWriter, r *http.Request) {
	run, accepted, err := application.startEvaluationRun(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
	_ = accepted
}

func (application *Application) startEvaluationRun(ctx context.Context) (evaluationRunResponse, bool, error) {
	application.evaluationMu.Lock()
	if application.runID != "" {
		id := application.runID
		application.pendingRun = true
		application.evaluationMu.Unlock()
		run, err := application.readEvaluationRun(ctx, id)
		return run, false, err
	}
	id, err := randomID()
	if err != nil {
		application.evaluationMu.Unlock()
		return evaluationRunResponse{}, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = application.database.ExecContext(ctx, `INSERT INTO evaluation_runs(id, status, started_at) VALUES (?, 'running', ?)`, id, now); err != nil {
		application.evaluationMu.Unlock()
		return evaluationRunResponse{}, false, err
	}
	application.runID = id
	application.evaluationMu.Unlock()
	go application.executeEvaluationRun(context.Background(), id)
	run, err := application.readEvaluationRun(ctx, id)
	return run, true, err
}

func (application *Application) handleCurrentEvaluationRun(w http.ResponseWriter, r *http.Request) {
	application.evaluationMu.Lock()
	id := application.runID
	application.evaluationMu.Unlock()
	if id != "" {
		run, err := application.readEvaluationRun(r.Context(), id)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, run)
		return
	}
	var run evaluationRun
	err := application.database.QueryRowContext(r.Context(), `SELECT id, status, started_at, finished_at, total, passed, failed FROM evaluation_runs ORDER BY started_at DESC LIMIT 1`).Scan(&run.ID, &run.Status, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, evaluationRunResponse{evaluationRun: evaluationRun{Status: "idle"}, Results: []evaluationNodeResult{}})
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, application.runResponse(r.Context(), run))
}

func (application *Application) handleGetEvaluationRun(w http.ResponseWriter, r *http.Request) {
	run, err := application.readEvaluationRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, errors.New("Evaluation Run not found"))
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, run)
}

func (application *Application) readEvaluationRun(ctx context.Context, id string) (evaluationRunResponse, error) {
	var run evaluationRun
	if err := application.database.QueryRowContext(ctx, `SELECT id, status, started_at, finished_at, total, passed, failed FROM evaluation_runs WHERE id = ?`, id).Scan(&run.ID, &run.Status, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed); err != nil {
		return evaluationRunResponse{}, err
	}
	return application.runResponse(ctx, run), nil
}

func (application *Application) runResponse(ctx context.Context, run evaluationRun) evaluationRunResponse {
	result := evaluationRunResponse{evaluationRun: run, Results: []evaluationNodeResult{}}
	rows, err := application.database.QueryContext(ctx, `SELECT node_id, name, state, attempts, successful, median_latency_ms, exit_identity, address_family, ip_score, reason FROM evaluation_results WHERE run_id = ? ORDER BY name`, run.ID)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var item evaluationNodeResult
		if rows.Scan(&item.NodeID, &item.Name, &item.State, &item.Attempts, &item.Successful, &item.MedianLatencyMS, &item.ExitIdentity, &item.AddressFamily, &item.IPScore, &item.Reason) == nil {
			result.Results = append(result.Results, item)
		}
	}
	return result
}

func (application *Application) executeEvaluationRun(ctx context.Context, id string) {
	defer func() {
		application.cleanupEvaluationHistory(ctx)
		application.evaluationMu.Lock()
		pending := application.pendingRun
		application.pendingRun = false
		if pending {
			nextID, err := randomID()
			if err == nil {
				now := time.Now().UTC().Format(time.RFC3339Nano)
				if _, err = application.database.ExecContext(ctx, `INSERT INTO evaluation_runs(id, status, started_at) VALUES (?, 'running', ?)`, nextID, now); err == nil {
					application.runID = nextID
					application.evaluationMu.Unlock()
					go application.executeEvaluationRun(context.Background(), nextID)
					return
				}
			}
		}
		application.runID = ""
		application.evaluationMu.Unlock()
	}()
	nodes, err := application.evaluationNodes(ctx)
	if err != nil {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, err)
		return
	}
	passed, failed := 0, 0
	for _, node := range nodes {
		item := application.evaluateNode(ctx, node)
		if item.State == "passed" {
			passed++
		} else {
			failed++
		}
		_, _ = application.database.ExecContext(ctx, `INSERT INTO evaluation_results(run_id, node_id, name, state, attempts, successful, median_latency_ms, exit_identity, address_family, ip_score, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, item.NodeID, item.Name, item.State, item.Attempts, item.Successful, item.MedianLatencyMS, item.ExitIdentity, item.AddressFamily, item.IPScore, item.Reason)
		_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET total = ?, passed = ?, failed = ? WHERE id = ?`, passed+failed, passed, failed, id)
	}
	if passed > 0 {
		if err := application.publishQualifiedNodes(ctx, id); err != nil {
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, err)
			return
		}
	}
	application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, nil)
}

func (application *Application) runScheduler() {
	for {
		minutes := 360
		_ = application.database.QueryRowContext(application.lifecycleCtx, `SELECT value FROM settings WHERE key = 'evaluation_interval_minutes'`).Scan(&minutes)
		if minutes <= 0 {
			select {
			case <-application.lifecycleCtx.Done():
				return
			case <-time.After(time.Minute):
				continue
			}
		}
		timer := time.NewTimer(time.Duration(minutes) * time.Minute)
		select {
		case <-application.lifecycleCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
			_, _, _ = application.startEvaluationRun(application.lifecycleCtx)
		}
	}
}

func (application *Application) cleanupEvaluationHistory(ctx context.Context) {
	days := 7
	_ = application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'history_retention_days'`).Scan(&days)
	if days < 3 || days > 7 {
		days = 7
	}
	_, _ = application.database.ExecContext(ctx, `DELETE FROM evaluation_results WHERE run_id IN (SELECT id FROM evaluation_runs WHERE julianday(started_at) < julianday('now', ?))`, fmt.Sprintf("-%d days", days))
	_, _ = application.database.ExecContext(ctx, `DELETE FROM evaluation_runs WHERE julianday(started_at) < julianday('now', ?)`, fmt.Sprintf("-%d days", days))
}

func (application *Application) publishQualifiedNodes(ctx context.Context, runID string) error {
	rows, err := application.database.QueryContext(ctx, `SELECT p.config FROM evaluation_results r JOIN proxy_nodes p ON p.id = r.node_id WHERE r.run_id = ? AND r.state = 'passed' ORDER BY r.name`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	proxies := make([]map[string]any, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return err
		}
		var config map[string]any
		if err := yaml.Unmarshal(data, &config); err != nil {
			return err
		}
		proxies = append(proxies, config)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(proxies) == 0 {
		return nil
	}
	groups := []map[string]any{
		{"name": "AUTO", "type": "url-test", "proxies": proxyNames(proxies), "url": "https://www.gstatic.com/generate_204", "interval": 300},
		{"name": "FALLBACK", "type": "fallback", "proxies": proxyNames(proxies), "url": "https://www.gstatic.com/generate_204", "interval": 300},
		{"name": "SELECT", "type": "select", "proxies": append([]string{"AUTO", "FALLBACK", "DIRECT"}, proxyNames(proxies)...)},
	}
	document, err := yaml.Marshal(map[string]any{"proxies": proxies, "proxy-groups": groups})
	if err != nil {
		return err
	}
	if err := application.dependencies.Kernel.Validate(ctx, document); err != nil {
		return fmt.Errorf("validate Published Subscription: %w", err)
	}
	_, err = application.database.ExecContext(ctx, `INSERT INTO publications(id, document, updated_at) VALUES (1, ?, ?) ON CONFLICT(id) DO UPDATE SET document = excluded.document, updated_at = excluded.updated_at`, document, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func proxyNames(proxies []map[string]any) []string {
	names := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		if name, ok := proxy["name"].(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (application *Application) finishEvaluationRun(ctx context.Context, id string, total, passed, failed int, runErr error) {
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET status = ?, total = ?, passed = ?, failed = ?, finished_at = ? WHERE id = ?`, status, total, passed, failed, time.Now().UTC().Format(time.RFC3339Nano), id)
}

func (application *Application) evaluationNodes(ctx context.Context) ([]evaluationNode, error) {
	rows, err := application.database.QueryContext(ctx, `SELECT p.id, p.name, p.config FROM proxy_nodes p JOIN upstream_subscriptions s ON s.id = p.subscription_id WHERE p.state = 'accepted' AND s.enabled = 1 AND julianday(s.last_success_at) >= julianday('now', '-7 days') ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]evaluationNode, 0)
	for rows.Next() {
		var node evaluationNode
		var config []byte
		if err := rows.Scan(&node.ID, &node.Name, &config); err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(config, &node.Config); err != nil {
			return nil, err
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

func (application *Application) evaluateNode(ctx context.Context, node evaluationNode) evaluationNodeResult {
	result := evaluationNodeResult{NodeID: node.ID, Name: node.Name, State: "failed"}
	channel, ok := application.dependencies.TestChannel.(AvailabilityChannel)
	if !ok {
		result.Reason = "test_channel_unverified: availability channel cannot prove Proxy Node ownership"
		return result
	}
	latencies := make([]time.Duration, 0, DefaultAvailabilityAttempts)
	for attempt := 0; attempt < DefaultAvailabilityAttempts; attempt++ {
		result.Attempts++
		var lastErr error
		for _, target := range DefaultAvailabilityURLs {
			probeCtx, cancel := context.WithTimeout(ctx, DefaultAvailabilityTimeout)
			probe, err := channel.ProbeAttempt(probeCtx, ProxyNode{Name: node.Name, Config: node.Config}, target)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			if !probe.Verified {
				result.Reason = "test_channel_unverified: request ownership could not be proven"
				return result
			}
			if probe.Success {
				latencies = append(latencies, probe.Latency)
				result.Successful++
				result.ExitIdentity = probe.ExitIdentity
				break
			}
		}
		if lastErr != nil && result.Reason == "" {
			result.Reason = "probe_failed: " + lastErr.Error()
		}
	}
	if len(latencies) > 0 {
		result.MedianLatencyMS = medianLatency(latencies).Seconds() * 1000
	}
	if result.Successful < 2 {
		if result.Reason == "" {
			result.Reason = fmt.Sprintf("insufficient_successes: %d/%d", result.Successful, DefaultAvailabilityAttempts)
		}
		return result
	}
	if result.MedianLatencyMS > DefaultAvailabilityMaxLatency.Seconds()*1000 {
		result.Reason = "latency_exceeded: median latency is above 1500ms"
		return result
	}
	if result.ExitIdentity == "" {
		result.Reason = "no_exit_identity: Test Channel returned no exit identity"
		return result
	}
	score, family, err := application.scoreNode(ctx, result.ExitIdentity, node, channel)
	result.AddressFamily = family
	if err != nil {
		result.Reason = "score_unavailable: " + err.Error()
		return result
	}
	result.IPScore = &score
	threshold := application.scoringThreshold(ctx, scoringProviderKey(application.dependencies.Scoring))
	if configured, err := application.configuredScoringProvider(ctx); err == nil {
		threshold = application.scoringThreshold(ctx, scoringProviderKey(configured))
	}
	if score < float64(threshold) {
		result.Reason = fmt.Sprintf("low_score: IP Score %.2f is below threshold %d", score, threshold)
		return result
	}
	result.State = "passed"
	return result
}

func (application *Application) configuredScoringProvider(ctx context.Context) (ScoringProvider, error) {
	var name string
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'scoring_provider'`).Scan(&name); err != nil {
		return application.dependencies.Scoring, err
	}
	if provider, ok := application.dependencies.ScoringProviders[name]; ok {
		return provider, nil
	}
	if name == "iplark" && application.dependencies.Scoring != nil {
		return application.dependencies.Scoring, nil
	}
	return nil, fmt.Errorf("Scoring Provider %q is not configured", name)
}

func (application *Application) scoringThreshold(ctx context.Context, provider string) int {
	key := "ipcheck_threshold"
	if provider == "iplark" {
		key = "iplark_threshold"
	}
	var value int
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value); err != nil || value < 0 || value > 100 {
		return 70
	}
	return value
}

func (application *Application) scoreNode(ctx context.Context, exitIdentity string, node evaluationNode, channel AvailabilityChannel) (float64, string, error) {
	providerValue := application.dependencies.Scoring
	if configured, err := application.configuredScoringProvider(ctx); err == nil {
		providerValue = configured
	}
	if provider, ok := providerValue.(ChannelScoringProvider); ok {
		transportProvider, hasTransport := channel.(TestChannelHTTPClient)
		if !hasTransport {
			return 0, addressFamily(exitIdentity), errors.New("Test Channel cannot provide scoring transport")
		}
		client, err := transportProvider.HTTPClient(ctx, ProxyNode{Name: node.Name, Config: node.Config})
		if err != nil {
			return 0, addressFamily(exitIdentity), err
		}
		return application.scoreWithCacheUsing(ctx, exitIdentity, providerValue, func() (float64, error) { return provider.ScoreWithClient(ctx, exitIdentity, client) })
	}
	return application.scoreWithCacheUsing(ctx, exitIdentity, providerValue, func() (float64, error) { return providerValue.Score(ctx, exitIdentity) })
}

func medianLatency(values []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func (application *Application) scoreWithCache(ctx context.Context, exitIdentity string) (float64, string, error) {
	provider := application.dependencies.Scoring
	return application.scoreWithCacheUsing(ctx, exitIdentity, provider, func() (float64, error) { return provider.Score(ctx, exitIdentity) })
}

func (application *Application) scoreWithCacheUsing(ctx context.Context, exitIdentity string, provider ScoringProvider, scoreProvider func() (float64, error)) (float64, string, error) {
	family := addressFamily(exitIdentity)
	if family == "" {
		return 0, "", errors.New("exit identity is not a valid IP address")
	}
	providerKey := scoringProviderKey(provider)
	var score float64
	var scoredAt string
	err := application.database.QueryRowContext(ctx, `SELECT score, scored_at FROM score_cache WHERE provider = ? AND exit_identity = ?`, providerKey, exitIdentity).Scan(&score, &scoredAt)
	if err == nil {
		when, parseErr := time.Parse(time.RFC3339Nano, scoredAt)
		if parseErr == nil && time.Since(when) <= 24*time.Hour {
			return score, family, nil
		}
	}
	score, err = scoreProvider()
	if err != nil {
		return 0, family, err
	}
	_, err = application.database.ExecContext(ctx, `INSERT INTO score_cache(provider, exit_identity, score, address_family, scored_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(provider, exit_identity) DO UPDATE SET score = excluded.score, address_family = excluded.address_family, scored_at = excluded.scored_at`, providerKey, exitIdentity, score, family, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, family, fmt.Errorf("store IP Score cache: %w", err)
	}
	return score, family, nil
}

func scoringProviderKey(provider ScoringProvider) string {
	if named, ok := provider.(interface{ Name() string }); ok && named.Name() != "" {
		return named.Name()
	}
	return reflect.TypeOf(provider).String()
}

func addressFamily(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		return "ipv4"
	}
	return "ipv6"
}
