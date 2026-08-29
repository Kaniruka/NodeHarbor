package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type evaluationRun struct {
	ID                string  `json:"id"`
	Status            string  `json:"status"`
	Trigger           string  `json:"trigger"`
	Phase             string  `json:"phase,omitempty"`
	StartedAt         string  `json:"startedAt"`
	FinishedAt        string  `json:"finishedAt,omitempty"`
	DurationMS        float64 `json:"durationMs"`
	Total             int     `json:"total"`
	Passed            int     `json:"passed"`
	Failed            int     `json:"failed"`
	PublicationResult string  `json:"publicationResult"`
	Reason            string  `json:"reason,omitempty"`
	FailureSummary    string  `json:"failureSummary,omitempty"`
}
type evaluationPhase struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt string  `json:"finishedAt,omitempty"`
	DurationMS float64 `json:"durationMs"`
	Reason     string  `json:"reason,omitempty"`
}
type evaluationLog struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	RunID     string `json:"runId,omitempty"`
	Message   string `json:"message"`
}
type evaluationNodeResult struct {
	NodeID          string         `json:"nodeId"`
	Name            string         `json:"name"`
	Config          map[string]any `json:"-"`
	State           string         `json:"state"`
	Attempts        int            `json:"attempts"`
	Successful      int            `json:"successful"`
	MedianLatencyMS float64        `json:"medianLatencyMs"`
	ExitIdentity    string         `json:"exitIdentity,omitempty"`
	AddressFamily   string         `json:"addressFamily,omitempty"`
	IPScore         *float64       `json:"ipScore,omitempty"`
	ScoreSource     string         `json:"scoreSource,omitempty"`
	Reason          string         `json:"reason,omitempty"`
}
type evaluationRunResponse struct {
	evaluationRun
	Results []evaluationNodeResult  `json:"results"`
	Sources []evaluationSourceState `json:"sources"`
	Phases  []evaluationPhase       `json:"phases"`
}
type evaluationSourceState struct {
	ID             string `json:"sourceId"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Enabled        bool   `json:"enabled"`
	RefreshStatus  string `json:"refreshStatus"`
	LastError      string `json:"lastError,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	ProxyNodeCount int    `json:"proxyNodeCount"`
}
type evaluationNode struct {
	ID     string
	Name   string
	Config map[string]any
}

type availabilityConfig struct {
	attempts          int
	requiredSuccesses int
	timeout           time.Duration
	maxLatency        time.Duration
	urls              []string
	workerCount       int
	scoringJitter     time.Duration
	scoreCacheTTL     time.Duration
}

var errScoringProviderUnavailable = errors.New("scoring provider unavailable")

func (application *Application) readAvailabilityConfig(ctx context.Context) (availabilityConfig, error) {
	settings, err := application.readSettings(ctx)
	if err != nil {
		return availabilityConfig{}, err
	}
	config := availabilityConfig{
		attempts:          settings.AvailabilityAttempts,
		requiredSuccesses: settings.AvailabilityRequiredSuccesses,
		timeout:           time.Duration(settings.AvailabilityTimeoutSecs) * time.Second,
		maxLatency:        time.Duration(settings.AvailabilityMaxLatencyMS) * time.Millisecond,
		urls:              append([]string(nil), settings.AvailabilityURLs...),
		workerCount:       settings.EvaluationWorkerCount,
		scoringJitter:     time.Duration(settings.ScoringJitterMS) * time.Millisecond,
		scoreCacheTTL:     time.Duration(settings.ScoreCacheTTLMinutes) * time.Minute,
	}
	if config.attempts < 1 || config.requiredSuccesses < 1 || config.requiredSuccesses > config.attempts || config.timeout <= 0 || config.maxLatency <= 0 || len(config.urls) == 0 || config.workerCount < 1 || config.workerCount > 3 || config.scoringJitter < 0 || config.scoreCacheTTL <= 0 {
		return availabilityConfig{}, errors.New("invalid Availability Check settings")
	}
	return config, nil
}

func (application *Application) initializeEvaluationRuns(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS evaluation_runs (id TEXT PRIMARY KEY, status TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT 'manual', phase TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', total INTEGER NOT NULL DEFAULT 0, passed INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, publication_result TEXT NOT NULL DEFAULT 'not_attempted', reason TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS evaluation_results (run_id TEXT NOT NULL, node_id TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL, successful INTEGER NOT NULL, median_latency_ms REAL NOT NULL DEFAULT 0, exit_identity TEXT NOT NULL DEFAULT '', address_family TEXT NOT NULL DEFAULT '', ip_score REAL, score_source TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', PRIMARY KEY(run_id, node_id))`,
		`CREATE TABLE IF NOT EXISTS evaluation_sources (run_id TEXT NOT NULL, source_id TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, enabled INTEGER NOT NULL, refresh_status TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '', last_success_at TEXT NOT NULL DEFAULT '', proxy_node_count INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(run_id, source_id))`,
		`CREATE TABLE IF NOT EXISTS score_cache (provider TEXT NOT NULL, exit_identity TEXT NOT NULL, score REAL NOT NULL, address_family TEXT NOT NULL, scored_at TEXT NOT NULL, PRIMARY KEY(provider, exit_identity))`,
		`CREATE TABLE IF NOT EXISTS evaluation_phases (run_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', PRIMARY KEY(run_id, name))`,
		`CREATE TABLE IF NOT EXISTS evaluation_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL DEFAULT '', timestamp TEXT NOT NULL, level TEXT NOT NULL, message TEXT NOT NULL)`,
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
	if !columns["config"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_results ADD COLUMN config BLOB NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !columns["ip_score"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_results ADD COLUMN ip_score REAL`); err != nil {
			return err
		}
	}
	if !columns["score_source"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_results ADD COLUMN score_source TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	var runColumns = map[string]bool{}
	runRows, err := application.database.QueryContext(ctx, `PRAGMA table_info(evaluation_runs)`)
	if err != nil {
		return err
	}
	for runRows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := runRows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			runRows.Close()
			return err
		}
		runColumns[name] = true
	}
	if err := runRows.Close(); err != nil {
		return err
	}
	if !runColumns["trigger"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_runs ADD COLUMN trigger TEXT NOT NULL DEFAULT 'manual'`); err != nil {
			return err
		}
	}
	if !runColumns["phase"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_runs ADD COLUMN phase TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !runColumns["publication_result"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_runs ADD COLUMN publication_result TEXT NOT NULL DEFAULT 'not_attempted'`); err != nil {
			return err
		}
	}
	if !runColumns["reason"] {
		if _, err := application.database.ExecContext(ctx, `ALTER TABLE evaluation_runs ADD COLUMN reason TEXT NOT NULL DEFAULT ''`); err != nil {
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

func (application *Application) handleExportSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := application.readSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (application *Application) handleListLogs(w http.ResponseWriter, r *http.Request) {
	rows, err := application.database.QueryContext(r.Context(), `SELECT timestamp, level, run_id, message FROM evaluation_logs ORDER BY id DESC LIMIT 200`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	logs := make([]evaluationLog, 0)
	for rows.Next() {
		var item evaluationLog
		if err := rows.Scan(&item.Timestamp, &item.Level, &item.RunID, &item.Message); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (application *Application) logEvaluation(ctx context.Context, runID, level, message string) {
	_, _ = application.database.ExecContext(ctx, `INSERT INTO evaluation_logs(run_id, timestamp, level, message) VALUES (?, ?, ?, ?)`, runID, application.clock.Now().UTC().Format(time.RFC3339Nano), level, message)
}

func (application *Application) beginEvaluationPhase(ctx context.Context, runID, name string) {
	now := application.clock.Now().UTC().Format(time.RFC3339Nano)
	_, _ = application.database.ExecContext(ctx, `INSERT INTO evaluation_phases(run_id, name, status, started_at) VALUES (?, ?, 'running', ?) ON CONFLICT(run_id, name) DO UPDATE SET status = 'running', started_at = excluded.started_at, finished_at = '', reason = ''`, runID, name, now)
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET phase = ? WHERE id = ?`, name, runID)
	application.logEvaluation(ctx, runID, "info", "phase started: "+name)
}

func (application *Application) finishEvaluationPhase(ctx context.Context, runID, name, status, reason string) {
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_phases SET status = ?, finished_at = ?, reason = ? WHERE run_id = ? AND name = ?`, status, application.clock.Now().UTC().Format(time.RFC3339Nano), reason, runID, name)
	application.logEvaluation(ctx, runID, "info", "phase "+status+": "+name)
}

func (application *Application) handleListEvaluationRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := application.database.QueryContext(r.Context(), `SELECT id, status, trigger, phase, started_at, finished_at, total, passed, failed, publication_result, reason FROM evaluation_runs ORDER BY started_at DESC LIMIT 50`)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	defer rows.Close()
	result := make([]evaluationRunResponse, 0)
	for rows.Next() {
		var run evaluationRun
		if err := rows.Scan(&run.ID, &run.Status, &run.Trigger, &run.Phase, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed, &run.PublicationResult, &run.Reason); err != nil {
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
	var input struct {
		IgnoreCache bool `json:"ignoreCache"`
	}
	if r.Body != nil {
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input)
		if err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, errors.New("invalid evaluation run request"))
			return
		}
	}
	run, accepted, err := application.startEvaluationRun(r.Context(), input.IgnoreCache, "manual")
	if err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
	_ = accepted
}

func (application *Application) startEvaluationRun(ctx context.Context, ignoreCache bool, trigger string) (evaluationRunResponse, bool, error) {
	application.evaluationMu.Lock()
	if application.closed {
		application.evaluationMu.Unlock()
		return evaluationRunResponse{}, false, errors.New("Application is closed")
	}
	if application.runID != "" {
		id := application.runID
		application.pendingRun = true
		application.pendingTrigger = "coalesced"
		application.pendingIgnoreCache = application.pendingIgnoreCache || ignoreCache
		application.evaluationMu.Unlock()
		run, err := application.readEvaluationRun(ctx, id)
		return run, false, err
	}
	id, err := randomID()
	if err != nil {
		application.evaluationMu.Unlock()
		return evaluationRunResponse{}, false, err
	}
	now := application.clock.Now().UTC().Format(time.RFC3339Nano)
	if _, err = application.database.ExecContext(ctx, `INSERT INTO evaluation_runs(id, status, trigger, started_at) VALUES (?, 'running', ?, ?)`, id, trigger, now); err != nil {
		application.evaluationMu.Unlock()
		return evaluationRunResponse{}, false, err
	}
	application.runID = id
	application.launchEvaluationRunLocked(id, ignoreCache)
	application.evaluationMu.Unlock()
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
	err := application.database.QueryRowContext(r.Context(), `SELECT id, status, trigger, phase, started_at, finished_at, total, passed, failed, publication_result, reason FROM evaluation_runs ORDER BY started_at DESC LIMIT 1`).Scan(&run.ID, &run.Status, &run.Trigger, &run.Phase, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed, &run.PublicationResult, &run.Reason)
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
	if err := application.database.QueryRowContext(ctx, `SELECT id, status, trigger, phase, started_at, finished_at, total, passed, failed, publication_result, reason FROM evaluation_runs WHERE id = ?`, id).Scan(&run.ID, &run.Status, &run.Trigger, &run.Phase, &run.StartedAt, &run.FinishedAt, &run.Total, &run.Passed, &run.Failed, &run.PublicationResult, &run.Reason); err != nil {
		return evaluationRunResponse{}, err
	}
	return application.runResponse(ctx, run), nil
}

func (application *Application) runResponse(ctx context.Context, run evaluationRun) evaluationRunResponse {
	result := evaluationRunResponse{evaluationRun: run, Results: []evaluationNodeResult{}, Sources: []evaluationSourceState{}, Phases: []evaluationPhase{}}
	result.FailureSummary = run.Reason
	started, startErr := time.Parse(time.RFC3339Nano, run.StartedAt)
	if startErr == nil {
		end := application.clock.Now().UTC()
		if run.FinishedAt != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, run.FinishedAt); err == nil {
				end = parsed
			}
		}
		if end.Before(started) {
			end = started
		}
		result.DurationMS = end.Sub(started).Seconds() * 1000
	}
	rows, err := application.database.QueryContext(ctx, `SELECT node_id, name, state, attempts, successful, median_latency_ms, exit_identity, address_family, ip_score, score_source, reason FROM evaluation_results WHERE run_id = ? ORDER BY name`, run.ID)
	if err == nil {
		for rows.Next() {
			var item evaluationNodeResult
			if rows.Scan(&item.NodeID, &item.Name, &item.State, &item.Attempts, &item.Successful, &item.MedianLatencyMS, &item.ExitIdentity, &item.AddressFamily, &item.IPScore, &item.ScoreSource, &item.Reason) == nil {
				result.Results = append(result.Results, item)
			}
		}
		_ = rows.Close()
	}
	sourceRows, err := application.database.QueryContext(ctx, `SELECT source_id, name, kind, enabled, refresh_status, last_error, last_success_at, proxy_node_count FROM evaluation_sources WHERE run_id = ? ORDER BY name, source_id`, run.ID)
	if err != nil {
		return result
	}
	defer sourceRows.Close()
	for sourceRows.Next() {
		var source evaluationSourceState
		var enabled int
		if sourceRows.Scan(&source.ID, &source.Name, &source.Kind, &enabled, &source.RefreshStatus, &source.LastError, &source.LastSuccessAt, &source.ProxyNodeCount) == nil {
			source.Enabled = enabled != 0
			result.Sources = append(result.Sources, source)
		}
	}
	phaseRows, err := application.database.QueryContext(ctx, `SELECT name, status, started_at, finished_at, reason FROM evaluation_phases WHERE run_id = ? ORDER BY started_at`, run.ID)
	if err == nil {
		defer phaseRows.Close()
		for phaseRows.Next() {
			var phase evaluationPhase
			if phaseRows.Scan(&phase.Name, &phase.Status, &phase.StartedAt, &phase.FinishedAt, &phase.Reason) != nil {
				continue
			}
			phaseStart, startErr := time.Parse(time.RFC3339Nano, phase.StartedAt)
			phaseEnd := application.clock.Now().UTC()
			if phase.FinishedAt != "" {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, phase.FinishedAt); parseErr == nil {
					phaseEnd = parsed
				}
			}
			if startErr == nil && !phaseEnd.Before(phaseStart) {
				phase.DurationMS = phaseEnd.Sub(phaseStart).Seconds() * 1000
			}
			result.Phases = append(result.Phases, phase)
		}
	}
	return result
}

func (application *Application) recordEvaluationSourceStates(ctx context.Context, runID string) error {
	subscriptions, err := application.listUpstreamSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("list Upstream Subscriptions for run history: %w", err)
	}
	for _, subscription := range subscriptions {
		if _, err := application.database.ExecContext(ctx, `INSERT INTO evaluation_sources(run_id, source_id, name, kind, enabled, refresh_status, last_error, last_success_at, proxy_node_count) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, subscription.ID, subscription.Name, subscription.Kind, subscription.Enabled, subscription.RefreshStatus, subscription.LastError, subscription.LastSuccessAt, subscription.ProxyNodeCount); err != nil {
			return fmt.Errorf("store Upstream Subscription run status: %w", err)
		}
	}
	return nil
}

func (application *Application) executeEvaluationRun(ctx context.Context, id string, ignoreCache bool) {
	application.logEvaluation(ctx, id, "info", "Evaluation Run started")
	application.beginEvaluationPhase(ctx, id, "refresh")
	defer func() {
		application.cleanupEvaluationHistory(ctx)
		application.evaluationMu.Lock()
		pending := application.pendingRun
		pendingIgnoreCache := application.pendingIgnoreCache
		pendingTrigger := application.pendingTrigger
		application.pendingRun = false
		application.pendingIgnoreCache = false
		application.pendingTrigger = ""
		if pending {
			if application.lifecycleCtx.Err() != nil || application.closed {
				application.runID = ""
				application.evaluationMu.Unlock()
				return
			}
			nextID, err := randomID()
			if err == nil {
				now := application.clock.Now().UTC().Format(time.RFC3339Nano)
				if _, err = application.database.ExecContext(ctx, `INSERT INTO evaluation_runs(id, status, trigger, started_at) VALUES (?, 'running', ?, ?)`, nextID, pendingTrigger, now); err == nil {
					application.runID = nextID
					application.launchEvaluationRunLocked(nextID, pendingIgnoreCache)
					application.evaluationMu.Unlock()
					return
				}
			}
		}
		application.runID = ""
		application.evaluationMu.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, fmt.Errorf("evaluation interrupted: %w", err))
		return
	}
	if reason, paused := application.isolationFailure(ctx); paused {
		application.pauseEvaluationAtPhase(ctx, id, "refresh", reason)
		return
	}
	refreshed, err := application.refreshUpstreamSubscriptions(ctx)
	if err != nil {
		if sourceErr := application.recordEvaluationSourceStates(ctx, id); sourceErr != nil {
			err = errors.Join(err, sourceErr)
		}
		application.finishEvaluationRun(ctx, id, 0, 0, 0, err)
		return
	}
	if err := application.recordEvaluationSourceStates(ctx, id); err != nil {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, err)
		return
	}
	if !refreshed {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, errors.New("all Upstream Subscriptions failed to refresh"))
		return
	}
	if reason, paused := application.isolationFailure(ctx); paused {
		application.pauseEvaluationAtPhase(ctx, id, "refresh", reason)
		return
	}
	application.finishEvaluationPhase(ctx, id, "refresh", "completed", "")
	application.beginEvaluationPhase(ctx, id, "availability-and-scoring")
	nodes, err := application.evaluationNodes(ctx)
	if err != nil {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, err)
		return
	}
	runStartedAt := application.evaluationRunStartedAt(ctx, id)
	availability, err := application.readAvailabilityConfig(ctx)
	if err != nil {
		application.finishEvaluationRun(ctx, id, 0, 0, 0, err)
		return
	}
	passed, failed := 0, 0
	evaluated := make([]evaluationNodeResult, 0, len(nodes))
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	jobs := make(chan evaluationNode)
	results := make(chan evaluationNodeResult, len(nodes))
	isolationFailures := make(chan string, 1)
	go application.monitorIsolation(runContext, cancelRun, isolationFailures)
	var workers sync.WaitGroup
	for worker := 0; worker < availability.workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for node := range jobs {
				if reason, paused := application.isolationFailure(runContext); paused {
					select {
					case isolationFailures <- reason:
					default:
					}
					cancelRun()
					return
				}
				results <- application.evaluateNode(runContext, node, availability, ignoreCache, runStartedAt)
			}
		}()
	}
	go func() {
		for _, node := range nodes {
			select {
			case jobs <- node:
			case <-runContext.Done():
				close(jobs)
				return
			}
		}
		close(jobs)
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	for item := range results {
		if err := ctx.Err(); err != nil {
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, fmt.Errorf("evaluation interrupted: %w", err))
			return
		}
		evaluated = append(evaluated, item)
		config, configErr := yaml.Marshal(item.Config)
		if configErr != nil {
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, configErr)
			return
		}
		if _, err := application.database.ExecContext(ctx, `INSERT INTO evaluation_results(run_id, node_id, name, state, attempts, successful, median_latency_ms, exit_identity, address_family, ip_score, score_source, reason, config) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, item.NodeID, item.Name, item.State, item.Attempts, item.Successful, item.MedianLatencyMS, item.ExitIdentity, item.AddressFamily, item.IPScore, item.ScoreSource, item.Reason, config); err != nil {
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, fmt.Errorf("store Evaluation Result: %w", err))
			return
		}
		if item.State == "passed" {
			passed++
		} else {
			failed++
		}
		if _, err := application.database.ExecContext(ctx, `UPDATE evaluation_runs SET total = ?, passed = ?, failed = ? WHERE id = ?`, passed+failed, passed, failed, id); err != nil {
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, fmt.Errorf("update Evaluation Run: %w", err))
			return
		}
	}
	select {
	case reason := <-isolationFailures:
		application.pauseEvaluationRun(ctx, id, reason)
		return
	default:
	}
	if err := ctx.Err(); err != nil {
		application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, fmt.Errorf("evaluation interrupted: %w", err))
		return
	}
	if err := runContext.Err(); err != nil {
		application.pauseEvaluationRun(ctx, id, "Surfing isolation monitor could not prove a safe Evaluation Run")
		return
	}
	if reason, paused := application.isolationFailure(ctx); paused {
		application.pauseEvaluationRun(ctx, id, reason)
		return
	}
	if reason := application.evaluationFailureReason(ctx, evaluated); reason != nil {
		application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, reason)
		return
	}
	if len(nodes) > 0 && passed == 0 {
		application.finishEvaluationPhase(ctx, id, "availability-and-scoring", "completed", "no Qualified Nodes")
		application.setPublicationResult(ctx, id, "retained")
		application.completeEvaluationRunWithReason(ctx, id, len(nodes), passed, failed, "no Qualified Nodes; previous Publication Snapshot retained")
		return
	}
	if passed > 0 {
		application.finishEvaluationPhase(ctx, id, "availability-and-scoring", "completed", "")
		application.beginEvaluationPhase(ctx, id, "publication")
		if err := application.publishQualifiedNodes(runContext, id); err != nil {
			application.setPublicationResult(ctx, id, "failed")
			application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, err)
			return
		}
		application.finishEvaluationPhase(ctx, id, "publication", "completed", "")
		application.setPublicationResult(ctx, id, "published")
	} else {
		application.finishEvaluationPhase(ctx, id, "availability-and-scoring", "completed", "")
		application.setPublicationResult(ctx, id, "retained")
	}
	application.finishEvaluationRun(ctx, id, len(nodes), passed, failed, nil)
}

func (application *Application) isolationFailure(ctx context.Context) (string, bool) {
	if application.dependencies.Isolation == nil {
		return "", false
	}
	status, err := application.dependencies.Isolation.Check(ctx)
	if err != nil {
		return err.Error(), true
	}
	if status.Verified && status.Mode != "tun" && status.Mode != "unknown" {
		return "", false
	}
	reason := status.Reason
	if reason == "" {
		reason = "Surfing isolation could not be proven"
	}
	return reason, true
}

func (application *Application) monitorIsolation(ctx context.Context, cancel context.CancelFunc, failures chan<- string) {
	if application.dependencies.Isolation == nil {
		return
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reason, paused := application.isolationFailure(ctx)
			if !paused {
				continue
			}
			select {
			case failures <- reason:
			default:
			}
			cancel()
			return
		}
	}
}

func (application *Application) evaluationFailureReason(ctx context.Context, results []evaluationNodeResult) error {
	if len(results) == 0 {
		return nil
	}
	allScoringUnavailable := true
	hasProviderFailure := false
	reasons := make([]string, 0, len(results))
	for _, result := range results {
		if strings.HasPrefix(result.Reason, "isolation_paused:") {
			return fmt.Errorf("%s; previous Publication Snapshot retained", result.Reason)
		}
		if strings.HasPrefix(result.Reason, "provider_unavailable:") || strings.HasPrefix(result.Reason, "score_unavailable:") {
			reasons = append(reasons, result.Reason)
			continue
		}
		allScoringUnavailable = false
	}
	if !allScoringUnavailable {
		return nil
	}
	provider, err := application.configuredScoringProvider(ctx)
	if err == nil {
		var failure string
		if queryErr := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, scoringProviderKey(provider)+"_failure").Scan(&failure); queryErr == nil {
			hasProviderFailure = failure != ""
		}
	}
	if !hasProviderFailure {
		return nil
	}
	return fmt.Errorf("all scoring attempts failed; previous Publication Snapshot retained: %s", strings.Join(reasons, "; "))
}

func (application *Application) refreshUpstreamSubscriptions(ctx context.Context) (bool, error) {
	subscriptions, err := application.listUpstreamSubscriptions(ctx)
	if err != nil {
		return false, fmt.Errorf("list Upstream Subscriptions for refresh: %w", err)
	}
	enabled, successful := 0, 0
	failures := make([]string, 0)
	for _, subscription := range subscriptions {
		if !subscription.Enabled {
			continue
		}
		enabled++
		if subscription.Kind != upstreamKindURL {
			successful++
			continue
		}
		if _, _, err := application.synchronizeUpstreamSubscription(ctx, subscription); err != nil {
			// synchronizeUpstreamSubscription records the stale state and last
			// successful document. A single source failure must not discard
			// other candidates or the previous publication snapshot.
			failures = append(failures, fmt.Sprintf("%s: %v", subscription.Name, err))
			continue
		}
		successful++
	}
	if enabled > 0 && successful == 0 {
		return false, fmt.Errorf("all Upstream Subscriptions failed to refresh: %s", strings.Join(failures, "; "))
	}
	return true, nil
}

func (application *Application) launchEvaluationRunLocked(id string, ignoreCache bool) {
	application.evaluationWG.Add(1)
	go func() {
		defer application.evaluationWG.Done()
		application.executeEvaluationRun(application.lifecycleCtx, id, ignoreCache)
	}()
}

func (application *Application) evaluationRunStartedAt(ctx context.Context, id string) time.Time {
	var startedAt string
	if err := application.database.QueryRowContext(ctx, `SELECT started_at FROM evaluation_runs WHERE id = ?`, id).Scan(&startedAt); err == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
			return parsed
		}
	}
	return application.clock.Now().UTC()
}

func (application *Application) pauseEvaluationRun(ctx context.Context, id, reason string) {
	application.finishRunningPhases(ctx, id, "paused", reason)
	application.setPublicationResult(ctx, id, "retained")
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET status = 'paused', reason = ?, finished_at = ?, phase = '' WHERE id = ?`, reason, application.clock.Now().UTC().Format(time.RFC3339Nano), id)
}

func (application *Application) pauseEvaluationAtPhase(ctx context.Context, id, phase, reason string) {
	application.finishEvaluationPhase(ctx, id, phase, "paused", reason)
	application.pauseEvaluationRun(ctx, id, reason)
}

func (application *Application) runScheduler() {
	for {
		minutes := 360
		_ = application.database.QueryRowContext(application.lifecycleCtx, `SELECT value FROM settings WHERE key = 'evaluation_interval_minutes'`).Scan(&minutes)
		if minutes <= 0 {
			select {
			case <-application.lifecycleCtx.Done():
				return
			case <-application.scheduleWake:
				continue
			}
		}
		timer := application.clock.After(time.Duration(minutes) * time.Minute)
		select {
		case <-application.lifecycleCtx.Done():
			return
		case <-application.scheduleWake:
			continue
		case <-timer:
			_, _, _ = application.startEvaluationRun(application.lifecycleCtx, false, "scheduled")
		}
	}
}

func (application *Application) cleanupEvaluationHistory(ctx context.Context) {
	days := 7
	_ = application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'history_retention_days'`).Scan(&days)
	if days < 3 || days > 7 {
		days = 7
	}
	cutoff := application.clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	query := `DELETE FROM %s WHERE run_id IN (SELECT id FROM evaluation_runs WHERE started_at < ? AND status <> 'running')`
	for _, table := range []string{"evaluation_results", "evaluation_sources", "evaluation_phases", "evaluation_logs"} {
		_, _ = application.database.ExecContext(ctx, fmt.Sprintf(query, table), cutoff)
	}
	_, _ = application.database.ExecContext(ctx, `DELETE FROM evaluation_runs WHERE started_at < ? AND status <> 'running'`, cutoff)
}

func (application *Application) publishQualifiedNodes(ctx context.Context, runID string) error {
	rows, err := application.database.QueryContext(ctx, `SELECT r.config FROM evaluation_results r WHERE r.run_id = ? AND r.state = 'passed' ORDER BY r.name`, runID)
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
		if len(data) == 0 {
			return errors.New("qualified Proxy Node configuration snapshot is empty")
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
	_, err = application.database.ExecContext(ctx, `INSERT INTO publications(id, document, updated_at) VALUES (1, ?, ?) ON CONFLICT(id) DO UPDATE SET document = excluded.document, updated_at = excluded.updated_at`, document, application.clock.Now().UTC().Format(time.RFC3339Nano))
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
	reason := ""
	if runErr != nil {
		status = "failed"
		reason = runErr.Error()
	}
	application.finishRunningPhases(ctx, id, status, reason)
	application.writeEvaluationRunResult(ctx, id, total, passed, failed, status, reason)
}

func (application *Application) completeEvaluationRunWithReason(ctx context.Context, id string, total, passed, failed int, reason string) {
	application.writeEvaluationRunResult(ctx, id, total, passed, failed, "completed", reason)
}

func (application *Application) writeEvaluationRunResult(ctx context.Context, id string, total, passed, failed int, status, reason string) {
	if ctx.Err() != nil {
		status = "failed"
		reason = fmt.Sprintf("evaluation interrupted: %v", ctx.Err())
		ctx = context.Background()
	}
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET status = ?, total = ?, passed = ?, failed = ?, reason = ?, finished_at = ?, phase = '' WHERE id = ?`, status, total, passed, failed, reason, application.clock.Now().UTC().Format(time.RFC3339Nano), id)
	application.logEvaluation(ctx, id, "info", "Evaluation Run "+status)
}

func (application *Application) finishRunningPhases(ctx context.Context, runID, status, reason string) {
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_phases SET status = ?, finished_at = ?, reason = ? WHERE run_id = ? AND status = 'running'`, status, application.clock.Now().UTC().Format(time.RFC3339Nano), reason, runID)
}

func (application *Application) setPublicationResult(ctx context.Context, runID, result string) {
	_, _ = application.database.ExecContext(ctx, `UPDATE evaluation_runs SET publication_result = ? WHERE id = ?`, result, runID)
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

func (application *Application) evaluateNode(ctx context.Context, node evaluationNode, config availabilityConfig, ignoreCache bool, runStartedAt time.Time) (result evaluationNodeResult) {
	result = evaluationNodeResult{NodeID: node.ID, Name: node.Name, Config: cloneProxyNodeConfig(node.Config), State: "failed"}
	proxyNode := ProxyNode{Name: node.Name, Config: node.Config}
	if releaser, ok := application.dependencies.TestChannel.(interface{ Release(ProxyNode) error }); ok {
		defer func() {
			if err := releaser.Release(proxyNode); err != nil {
				result.State = "failed"
				if result.Reason == "" {
					result.Reason = "test_channel_cleanup_failed: " + err.Error()
				} else {
					result.Reason += "; test_channel_cleanup_failed: " + err.Error()
				}
			}
		}()
	}
	channel, ok := application.dependencies.TestChannel.(AvailabilityChannel)
	if !ok {
		result.Reason = "test_channel_unverified: availability channel cannot prove Proxy Node ownership"
		return result
	}
	latencies := make([]time.Duration, 0, config.attempts)
	identities := make([]ExitIdentityCandidate, 0)
	for attempt := 0; attempt < config.attempts; attempt++ {
		result.Attempts++
		var lastErr error
		probeCtx, cancel := context.WithTimeout(ctx, config.timeout)
		for _, target := range config.urls {
			if err := probeCtx.Err(); err != nil {
				lastErr = err
				break
			}
			probe, err := channel.ProbeAttempt(probeCtx, proxyNode, target)
			if err != nil {
				lastErr = err
				continue
			}
			if !probe.Verified {
				cancel()
				result.Reason = "test_channel_unverified: request ownership could not be proven"
				return result
			}
			if probe.Success {
				lastErr = nil
				cancel()
				latencies = append(latencies, probe.Latency)
				result.Successful++
				identities = append(identities, exitIdentityCandidates(probe)...)
				break
			}
		}
		cancel()
		if lastErr != nil && result.Reason == "" {
			result.Reason = "probe_failed: " + lastErr.Error()
		}
	}
	if len(latencies) > 0 {
		result.MedianLatencyMS = medianLatency(latencies).Seconds() * 1000
	}
	if result.Successful < config.requiredSuccesses {
		if result.Reason == "" {
			result.Reason = fmt.Sprintf("insufficient_successes: %d/%d", result.Successful, config.attempts)
		}
		return result
	}
	if result.MedianLatencyMS > config.maxLatency.Seconds()*1000 {
		result.Reason = fmt.Sprintf("latency_exceeded: median latency is above %.0fms", config.maxLatency.Seconds()*1000)
		return result
	}
	if reason, paused := application.isolationFailure(ctx); paused {
		result.Reason = "isolation_paused: " + reason
		return result
	}
	discoverer, ok := application.dependencies.TestChannel.(ExitIdentityDiscoveryChannel)
	if !ok {
		result.Reason = "test_channel_unverified: Test Channel does not expose family-specific Exit Identity discovery"
		return result
	}
	identities, identityErr := discoverExitIdentities(ctx, node, discoverer)
	selectedIdentity, family, identityErr := selectExitIdentity(identities)
	if identityErr == nil && selectedIdentity == "" && len(identities) == 0 {
		identityErr = errors.New("no_exit_identity: Test Channel returned no exit identity")
	}
	if identityErr != nil {
		result.Reason = identityErr.Error()
		return result
	}
	result.ExitIdentity = selectedIdentity
	result.AddressFamily = family
	if result.ExitIdentity == "" {
		result.Reason = "no_exit_identity: Test Channel returned no exit identity"
		return result
	}
	if reason, paused := application.isolationFailure(ctx); paused {
		result.Reason = "isolation_paused: " + reason
		return result
	}
	score, family, source, err := application.scoreNode(ctx, result.ExitIdentity, node, channel, config.scoringJitter, config.scoreCacheTTL, ignoreCache, runStartedAt)
	result.AddressFamily = family
	if err != nil {
		if errors.Is(err, errScoringProviderUnavailable) {
			result.Reason = "provider_unavailable: scoring provider request failed"
			return result
		}
		if strings.HasPrefix(err.Error(), "isolation_paused:") {
			result.Reason = err.Error()
			return result
		}
		result.Reason = "score_unavailable: " + err.Error()
		return result
	}
	result.IPScore = &score
	result.ScoreSource = source
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

func discoverExitIdentities(ctx context.Context, node evaluationNode, channel ExitIdentityDiscoveryChannel) ([]ExitIdentityCandidate, error) {
	ipv4, ipv4Err := channel.DiscoverExitIdentities(ctx, ProxyNode{Name: node.Name, Config: node.Config}, "ipv4")
	if ipv4Err == nil && containsVerifiedAddressFamily(ipv4, "ipv4") {
		return ipv4, nil
	}
	ipv6, ipv6Err := channel.DiscoverExitIdentities(ctx, ProxyNode{Name: node.Name, Config: node.Config}, "ipv6")
	if ipv6Err != nil {
		if containsAddressFamily(ipv4, "ipv4") {
			return ipv4, nil
		}
		if ipv4Err != nil {
			return nil, fmt.Errorf("IPv4 discovery failed: %v; IPv6 discovery failed: %w", ipv4Err, ipv6Err)
		}
		return nil, ipv6Err
	}
	return ipv6, nil
}

func containsAddressFamily(candidates []ExitIdentityCandidate, family string) bool {
	for _, candidate := range candidates {
		if addressFamily(candidate.IP) == family {
			return true
		}
	}
	return false
}

func containsVerifiedAddressFamily(candidates []ExitIdentityCandidate, family string) bool {
	for _, candidate := range candidates {
		if candidate.Verified && addressFamily(candidate.IP) == family {
			return true
		}
	}
	return false
}

func exitIdentityCandidates(attempt AvailabilityAttempt) []ExitIdentityCandidate {
	if len(attempt.ExitIdentities) > 0 {
		return append([]ExitIdentityCandidate(nil), attempt.ExitIdentities...)
	}
	if attempt.ExitIdentity == "" {
		return nil
	}
	return []ExitIdentityCandidate{{IP: attempt.ExitIdentity, Verified: attempt.Verified}}
}

func selectExitIdentity(candidates []ExitIdentityCandidate) (string, string, error) {
	if len(candidates) == 0 {
		return "", "", nil
	}
	var unverifiedFamily string
	for _, candidate := range candidates {
		family := addressFamily(candidate.IP)
		if family == "" {
			continue
		}
		if !candidate.Verified && unverifiedFamily == "" {
			unverifiedFamily = family
		}
	}
	for _, preferredFamily := range []string{"ipv4", "ipv6"} {
		for _, candidate := range candidates {
			if candidate.Verified && addressFamily(candidate.IP) == preferredFamily {
				return candidate.IP, preferredFamily, nil
			}
		}
	}
	if unverifiedFamily != "" {
		return "", "", errors.New("test_channel_unverified: exit identity ownership could not be proven")
	}
	return "", "", errors.New("no_exit_identity: Test Channel returned no valid exit identity")
}

func (application *Application) configuredScoringProvider(ctx context.Context) (ScoringProvider, error) {
	var name string
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'scoring_provider'`).Scan(&name); err != nil {
		return application.dependencies.Scoring, err
	}
	provider, ok := application.scoringProviderByName(name)
	if !ok {
		return nil, fmt.Errorf("Scoring Provider %q is not configured", name)
	}
	enabled, err := application.scoringProviderEnabled(ctx, name)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, fmt.Errorf("Scoring Provider %q is disabled", name)
	}
	return provider, nil
}

func (application *Application) scoringProviderByName(name string) (ScoringProvider, bool) {
	if provider, ok := application.dependencies.ScoringProviders[name]; ok && provider != nil {
		return provider, true
	}
	if name == "iplark" && application.dependencies.Scoring != nil {
		return application.dependencies.Scoring, true
	}
	return nil, false
}

func (application *Application) scoringProviderEnabled(ctx context.Context, name string) (bool, error) {
	var value string
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, name+"_enabled").Scan(&value); err != nil {
		return false, err
	}
	return value == "1" || strings.EqualFold(value, "true"), nil
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

func (application *Application) scoreNode(ctx context.Context, exitIdentity string, node evaluationNode, channel AvailabilityChannel, jitter, cacheTTL time.Duration, ignoreCache bool, runStartedAt time.Time) (float64, string, string, error) {
	providerValue, err := application.configuredScoringProvider(ctx)
	if err != nil {
		return 0, addressFamily(exitIdentity), "", fmt.Errorf("%w: %w", errScoringProviderUnavailable, err)
	}
	provider, ok := providerValue.(ChannelScoringProvider)
	if !ok {
		return 0, addressFamily(exitIdentity), "", errors.New("Scoring Provider cannot bind requests to the verified Test Channel")
	}
	return application.scoreWithCacheUsing(ctx, exitIdentity, providerValue, cacheTTL, ignoreCache, runStartedAt, func() (float64, error) {
		transportProvider, hasTransport := channel.(TestChannelHTTPClient)
		if !hasTransport {
			return 0, errors.New("Test Channel cannot provide scoring transport")
		}
		client, err := transportProvider.HTTPClient(ctx, ProxyNode{Name: node.Name, Config: node.Config})
		if err != nil {
			return 0, err
		}
		if err := waitScoringJitter(ctx, jitter); err != nil {
			return 0, err
		}
		if reason, paused := application.isolationFailure(ctx); paused {
			return 0, fmt.Errorf("isolation_paused: %s", reason)
		}
		score, err := provider.ScoreWithClient(ctx, exitIdentity, client)
		if err != nil {
			application.recordScoringProviderFailure(ctx, providerValue, err)
			return 0, err
		}
		application.clearScoringProviderFailure(ctx, providerValue)
		return score, nil
	})
}

func (application *Application) recordScoringProviderFailure(ctx context.Context, provider ScoringProvider, err error) {
	key := scoringProviderKey(provider) + "_failure"
	_, _ = application.database.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`, err.Error(), key)
}

func (application *Application) clearScoringProviderFailure(ctx context.Context, provider ScoringProvider) {
	key := scoringProviderKey(provider) + "_failure"
	_, _ = application.database.ExecContext(ctx, `UPDATE settings SET value = '' WHERE key = ?`, key)
}

func waitScoringJitter(ctx context.Context, maximum time.Duration) error {
	if maximum <= 0 {
		return nil
	}
	delay := time.Duration(rand.Int63n(int64(maximum) + 1))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func medianLatency(values []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return sorted[middle-1] + (sorted[middle]-sorted[middle-1])/2
}

func (application *Application) scoreWithCacheUsing(ctx context.Context, exitIdentity string, provider ScoringProvider, cacheTTL time.Duration, ignoreCache bool, runStartedAt time.Time, scoreProvider func() (float64, error)) (float64, string, string, error) {
	application.scoreCacheMu.Lock()
	defer application.scoreCacheMu.Unlock()
	family := addressFamily(exitIdentity)
	if family == "" {
		return 0, "", "", errors.New("exit identity is not a valid IP address")
	}
	providerKey := scoringProviderKey(provider)
	var score float64
	var scoredAt string
	err := application.database.QueryRowContext(ctx, `SELECT score, scored_at FROM score_cache WHERE provider = ? AND exit_identity = ?`, providerKey, exitIdentity).Scan(&score, &scoredAt)
	if err == nil {
		when, parseErr := time.Parse(time.RFC3339Nano, scoredAt)
		now := time.Now().UTC()
		if cacheTTL <= 0 {
			cacheTTL = 24 * time.Hour
		}
		cacheIsFresh := parseErr == nil && !when.After(now) && now.Sub(when) <= cacheTTL
		cacheWasWrittenThisRun := ignoreCache && parseErr == nil && !when.Before(runStartedAt) && !when.After(now)
		if cacheIsFresh && (!ignoreCache || cacheWasWrittenThisRun) {
			return score, family, "cache", nil
		}
	}
	score, err = scoreProvider()
	if err != nil {
		return 0, family, "", err
	}
	_, err = application.database.ExecContext(ctx, `INSERT INTO score_cache(provider, exit_identity, score, address_family, scored_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(provider, exit_identity) DO UPDATE SET score = excluded.score, address_family = excluded.address_family, scored_at = excluded.scored_at`, providerKey, exitIdentity, score, family, application.clock.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, family, "", fmt.Errorf("store IP Score cache: %w", err)
	}
	return score, family, "provider", nil
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
