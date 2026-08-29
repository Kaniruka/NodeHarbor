package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	urlpkg "net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrUnavailable = errors.New("adapter unavailable")

type ProxyNode struct {
	Name   string
	Config map[string]any
}

// NodeValidator is implemented by the bundled kernel. It deliberately sits
// beside Kernel so test doubles and older adapters can keep using whole-document
// validation while production gets isolated node-level validation.
type NodeValidator interface {
	ValidateNode(context.Context, ProxyNode) error
}

type proxyNodeState string

const (
	proxyNodeAccepted proxyNodeState = "accepted"
	proxyNodeRejected proxyNodeState = "rejected"
)

type evaluatedProxyNode struct {
	ID           string         `json:"id"`
	Fingerprint  string         `json:"fingerprint"`
	Name         string         `json:"name"`
	OriginalName string         `json:"originalName"`
	Config       map[string]any `json:"config"`
	State        proxyNodeState `json:"state"`
	Reason       string         `json:"reason,omitempty"`
	Rejection    *nodeRejection `json:"rejection,omitempty"`
}

type nodeRejection struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ProbeResult struct {
	ExitIdentity   string
	ExitIdentities []ExitIdentityCandidate
	Verified       bool
	Latency        time.Duration
}

// ExitIdentityCandidate is an address observed through the Test Channel.
// Verified must be true before the address can be scored or published.
type ExitIdentityCandidate struct {
	IP       string
	Verified bool
}

const (
	DefaultAvailabilityAttempts          = 3
	DefaultAvailabilityRequiredSuccesses = 2
	DefaultAvailabilityTimeout           = 5 * time.Second
	DefaultAvailabilityMaxLatency        = 1500 * time.Millisecond
	DefaultEvaluationWorkerCount         = 3
	DefaultScoringJitter                 = 100 * time.Millisecond
)

var DefaultAvailabilityURLs = []string{
	"https://www.gstatic.com/generate_204",
	"https://cp.cloudflare.com/generate_204",
}

func defaultAvailabilityURLsJSON() string {
	encoded, _ := json.Marshal(DefaultAvailabilityURLs)
	return string(encoded)
}

type AvailabilityAttempt struct {
	Success        bool
	Verified       bool
	Latency        time.Duration
	ExitIdentity   string
	ExitIdentities []ExitIdentityCandidate
}

type AvailabilityChannel interface {
	ProbeAttempt(context.Context, ProxyNode, string) (AvailabilityAttempt, error)
}

// ExitIdentityDiscoveryChannel explicitly requests an address family through
// the already-proven Test Channel. The coordinator asks for IPv4 first and
// asks for IPv6 only when no usable IPv4 candidate is available.
type ExitIdentityDiscoveryChannel interface {
	DiscoverExitIdentities(context.Context, ProxyNode, string) ([]ExitIdentityCandidate, error)
}

type TestChannelHTTPClient interface {
	// HTTPClient must return the transport established by the verified Test Channel.
	HTTPClient(context.Context, ProxyNode) (*http.Client, error)
}

type ChannelScoringProvider interface {
	// ScoreWithClient must send the provider request through the supplied
	// Test Channel HTTP client; direct provider clients are not accepted.
	ScoreWithClient(context.Context, string, *http.Client) (float64, error)
}

type SurfingIsolationStatus struct {
	Mode     string
	Verified bool
	Reason   string
}

type SurfingIsolationGuard interface {
	Check(context.Context) (SurfingIsolationStatus, error)
}

type UpstreamRequest struct {
	Location  string
	UserAgent string
}

type Upstream interface {
	Fetch(context.Context, UpstreamRequest) ([]byte, error)
}

type ScoringProvider interface {
	Score(context.Context, string) (float64, error)
}

type Kernel interface {
	Validate(context.Context, []byte) error
}

type TestChannel interface {
	Probe(context.Context, ProxyNode) (ProbeResult, error)
}

type Dependencies struct {
	Upstream         Upstream
	Scoring          ScoringProvider
	ScoringProviders map[string]ScoringProvider
	Kernel           Kernel
	TestChannel      TestChannel
	Isolation        SurfingIsolationGuard
}

type Config struct {
	DatabasePath        string
	WebAssets           fs.FS
	EnableTestEndpoints bool
}

type Application struct {
	database           *sql.DB
	handler            http.Handler
	dependencies       Dependencies
	evaluationMu       sync.Mutex
	evaluationWG       sync.WaitGroup
	scoreCacheMu       sync.Mutex
	runID              string
	pendingRun         bool
	pendingIgnoreCache bool
	closed             bool
	lifecycleCtx       context.Context
	stopScheduler      context.CancelFunc
}

type HealthComponent struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type healthResponse struct {
	Status                string          `json:"status"`
	Backend               HealthComponent `json:"backend"`
	Database              HealthComponent `json:"database"`
	PublishedSubscription HealthComponent `json:"publishedSubscription"`
}

type settingsResponse struct {
	Language                      string   `json:"language"`
	InstallationID                string   `json:"installationId"`
	ScoringProvider               string   `json:"scoringProvider"`
	IPLarkThreshold               int      `json:"iplarkThreshold"`
	IPCheckThreshold              int      `json:"ipcheckThreshold"`
	EvaluationIntervalMinutes     int      `json:"evaluationIntervalMinutes"`
	HistoryRetentionDays          int      `json:"historyRetentionDays"`
	AvailabilityAttempts          int      `json:"availabilityAttempts"`
	AvailabilityRequiredSuccesses int      `json:"availabilityRequiredSuccesses"`
	AvailabilityTimeoutSecs       int      `json:"availabilityTimeoutSeconds"`
	AvailabilityMaxLatencyMS      int      `json:"availabilityMaxLatencyMs"`
	AvailabilityURLs              []string `json:"availabilityURLs"`
	EvaluationWorkerCount         int      `json:"evaluationWorkerCount"`
	ScoringJitterMS               int      `json:"scoringJitterMs"`
}

func Open(ctx context.Context, config Config, dependencies Dependencies) (*Application, error) {
	if config.DatabasePath == "" {
		return nil, errors.New("database path is required")
	}
	if config.WebAssets == nil {
		return nil, errors.New("web assets are required")
	}
	if dependencies.Upstream == nil || dependencies.Scoring == nil || dependencies.Kernel == nil || dependencies.TestChannel == nil {
		return nil, errors.New("all evaluation adapters are required")
	}
	if err := ensureParentDirectory(config.DatabasePath); err != nil {
		return nil, err
	}

	database, err := sql.Open("sqlite", config.DatabasePath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	lifecycleCtx, stopScheduler := context.WithCancel(context.Background())
	application := &Application{database: database, dependencies: dependencies, lifecycleCtx: lifecycleCtx, stopScheduler: stopScheduler}
	if err := application.initialize(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	application.handler = application.routes(config)
	return application, nil
}

func (application *Application) Handler() http.Handler { return application.handler }

func (application *Application) Close() error {
	if application.stopScheduler != nil {
		application.stopScheduler()
	}
	application.evaluationMu.Lock()
	application.closed = true
	application.evaluationMu.Unlock()
	application.evaluationWG.Wait()
	closeErr := application.database.Close()
	if channel, ok := application.dependencies.TestChannel.(interface{ Close() error }); ok {
		closeErr = errors.Join(closeErr, channel.Close())
	}
	return closeErr
}

func (application *Application) initialize(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS publications (id INTEGER PRIMARY KEY CHECK (id = 1), document BLOB NOT NULL, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := application.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	if _, err := application.database.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES ('language', 'auto')`); err != nil {
		return fmt.Errorf("initialize language: %w", err)
	}
	for key, value := range map[string]string{
		"scoring_provider":                "iplark",
		"iplark_threshold":                "70",
		"ipcheck_threshold":               "70",
		"evaluation_interval_minutes":     "360",
		"history_retention_days":          "7",
		"availability_attempts":           fmt.Sprint(DefaultAvailabilityAttempts),
		"availability_required_successes": fmt.Sprint(DefaultAvailabilityRequiredSuccesses),
		"availability_timeout_seconds":    fmt.Sprint(int(DefaultAvailabilityTimeout / time.Second)),
		"availability_max_latency_ms":     fmt.Sprint(int(DefaultAvailabilityMaxLatency / time.Millisecond)),
		"availability_urls":               defaultAvailabilityURLsJSON(),
		"evaluation_worker_count":         fmt.Sprint(DefaultEvaluationWorkerCount),
		"scoring_jitter_ms":               fmt.Sprint(int(DefaultScoringJitter / time.Millisecond)),
	} {
		if _, err := application.database.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`, key, value); err != nil {
			return fmt.Errorf("initialize scoring setting: %w", err)
		}
	}
	installationID, err := randomID()
	if err != nil {
		return fmt.Errorf("generate installation id: %w", err)
	}
	if _, err := application.database.ExecContext(ctx, `INSERT OR IGNORE INTO system_state(key, value) VALUES ('installation_id', ?)`, installationID); err != nil {
		return fmt.Errorf("initialize system state: %w", err)
	}
	if err := application.initializeUpstreamSubscriptions(ctx); err != nil {
		return err
	}
	if err := application.initializeEvaluationRuns(ctx); err != nil {
		return err
	}
	go application.runScheduler()
	return application.ensureInitialPublication(ctx)
}

func (application *Application) ensureInitialPublication(ctx context.Context) error {
	var count int
	if err := application.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications`).Scan(&count); err != nil {
		return fmt.Errorf("inspect publication: %w", err)
	}
	if count != 0 {
		return nil
	}
	document := initialPublishedSubscription()
	if err := application.dependencies.Kernel.Validate(ctx, document); err != nil {
		return fmt.Errorf("validate initial published subscription: %w", err)
	}
	if _, err := application.database.ExecContext(ctx, `INSERT INTO publications(id, document, updated_at) VALUES (1, ?, ?)`, document, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("store initial published subscription: %w", err)
	}
	return nil
}

func initialPublishedSubscription() []byte {
	return []byte("proxies: []\nproxy-groups:\n  - name: AUTO\n    type: url-test\n    proxies: [DIRECT]\n    url: https://www.gstatic.com/generate_204\n    interval: 300\n  - name: FALLBACK\n    type: fallback\n    proxies: [DIRECT]\n    url: https://www.gstatic.com/generate_204\n    interval: 300\n  - name: SELECT\n    type: select\n    proxies: [AUTO, FALLBACK, DIRECT]\n")
}

func (application *Application) routes(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", application.handleHealth)
	mux.HandleFunc("GET /api/settings", application.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", application.handlePutSettings)
	mux.HandleFunc("GET /sub/clash.yaml", application.handlePublishedSubscription)
	application.registerUpstreamSubscriptionRoutes(mux)
	application.registerEvaluationRoutes(mux)
	if config.EnableTestEndpoints {
		mux.HandleFunc("POST /_test/evaluation", application.handleTestEvaluation)
	}
	mux.Handle("/", http.FileServer(http.FS(config.WebAssets)))
	return loopbackManagementOnly(mux)
}

func (application *Application) handleHealth(response http.ResponseWriter, request *http.Request) {
	health := healthResponse{
		Status:                "healthy",
		Backend:               HealthComponent{Status: "healthy"},
		Database:              HealthComponent{Status: "healthy"},
		PublishedSubscription: HealthComponent{Status: "healthy"},
	}
	if err := application.database.PingContext(request.Context()); err != nil {
		health.Status = "unhealthy"
		health.Database = HealthComponent{Status: "unhealthy", Message: err.Error()}
	}
	var publicationCount int
	if err := application.database.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM publications WHERE length(document) > 0`).Scan(&publicationCount); err != nil || publicationCount != 1 {
		health.Status = "unhealthy"
		health.PublishedSubscription = HealthComponent{Status: "unhealthy", Message: "no publication snapshot is available"}
	}
	writeJSON(response, http.StatusOK, health)
}

func (application *Application) handleGetSettings(response http.ResponseWriter, request *http.Request) {
	settings, err := application.readSettings(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	writeJSON(response, http.StatusOK, settings)
}

func (application *Application) handlePutSettings(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Language                      string    `json:"language"`
		ScoringProvider               string    `json:"scoringProvider"`
		IPLarkThreshold               *int      `json:"iplarkThreshold"`
		IPCheckThreshold              *int      `json:"ipcheckThreshold"`
		EvaluationIntervalMinutes     *int      `json:"evaluationIntervalMinutes"`
		HistoryRetentionDays          *int      `json:"historyRetentionDays"`
		AvailabilityAttempts          *int      `json:"availabilityAttempts"`
		AvailabilityRequiredSuccesses *int      `json:"availabilityRequiredSuccesses"`
		AvailabilityTimeoutSecs       *int      `json:"availabilityTimeoutSeconds"`
		AvailabilityMaxLatencyMS      *int      `json:"availabilityMaxLatencyMs"`
		AvailabilityURLs              *[]string `json:"availabilityURLs"`
		EvaluationWorkerCount         *int      `json:"evaluationWorkerCount"`
		ScoringJitterMS               *int      `json:"scoringJitterMs"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, errors.New("invalid settings document"))
		return
	}
	if input.Language != "zh-CN" && input.Language != "en" {
		if input.Language != "" {
			writeError(response, http.StatusBadRequest, errors.New("language must be zh-CN or en"))
			return
		}
	}
	if input.ScoringProvider != "" && input.ScoringProvider != "iplark" && input.ScoringProvider != "ipcheck" {
		writeError(response, http.StatusBadRequest, errors.New("scoringProvider must be iplark or ipcheck"))
		return
	}
	if input.AvailabilityURLs != nil {
		if len(*input.AvailabilityURLs) == 0 || len(*input.AvailabilityURLs) > 10 {
			writeError(response, http.StatusBadRequest, errors.New("availabilityURLs must contain between 1 and 10 URLs"))
			return
		}
		for _, target := range *input.AvailabilityURLs {
			parsed, err := urlpkg.ParseRequestURI(target)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				writeError(response, http.StatusBadRequest, errors.New("availabilityURLs must contain absolute HTTP(S) URLs"))
				return
			}
		}
	}
	attempts, required := DefaultAvailabilityAttempts, DefaultAvailabilityRequiredSuccesses
	if err := application.database.QueryRowContext(request.Context(), `SELECT value FROM settings WHERE key = 'availability_attempts'`).Scan(&attempts); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if err := application.database.QueryRowContext(request.Context(), `SELECT value FROM settings WHERE key = 'availability_required_successes'`).Scan(&required); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	if input.AvailabilityAttempts != nil {
		attempts = *input.AvailabilityAttempts
	}
	if input.AvailabilityRequiredSuccesses != nil {
		required = *input.AvailabilityRequiredSuccesses
	}
	if required > attempts {
		writeError(response, http.StatusBadRequest, errors.New("required availability successes cannot exceed availability attempts"))
		return
	}
	for name, value := range map[string]any{"language": input.Language, "scoring_provider": input.ScoringProvider, "iplark_threshold": input.IPLarkThreshold, "ipcheck_threshold": input.IPCheckThreshold, "evaluation_interval_minutes": input.EvaluationIntervalMinutes, "history_retention_days": input.HistoryRetentionDays, "availability_attempts": input.AvailabilityAttempts, "availability_required_successes": input.AvailabilityRequiredSuccesses, "availability_timeout_seconds": input.AvailabilityTimeoutSecs, "availability_max_latency_ms": input.AvailabilityMaxLatencyMS, "evaluation_worker_count": input.EvaluationWorkerCount, "scoring_jitter_ms": input.ScoringJitterMS} {
		if value == nil || value == "" {
			continue
		}
		stored := fmt.Sprint(value)
		if number, ok := value.(*int); ok {
			if number == nil {
				continue
			}
			valid := *number >= 0 && *number <= 100
			message := "score thresholds must be between 0 and 100"
			if name == "evaluation_interval_minutes" {
				valid = *number >= 0 && *number <= 10080
				message = "evaluation interval must be between 0 and 10080 minutes"
			}
			if name == "history_retention_days" {
				valid = *number >= 3 && *number <= 7
				message = "history retention must be between 3 and 7 days"
			}
			if name == "availability_attempts" {
				valid = *number >= 1 && *number <= 10
				message = "availability attempts must be between 1 and 10"
			}
			if name == "availability_required_successes" {
				valid = *number >= 1 && *number <= 10
				message = "required availability successes must be between 1 and 10"
			}
			if name == "availability_timeout_seconds" {
				valid = *number >= 1 && *number <= 300
				message = "availability timeout must be between 1 and 300 seconds"
			}
			if name == "availability_max_latency_ms" {
				valid = *number >= 1 && *number <= 60000
				message = "availability max latency must be between 1 and 60000 milliseconds"
			}
			if name == "evaluation_worker_count" {
				valid = *number >= 1 && *number <= 3
				message = "evaluation worker count must be between 1 and 3"
			}
			if name == "scoring_jitter_ms" {
				valid = *number >= 0 && *number <= 1000
				message = "scoring jitter must be between 0 and 1000 milliseconds"
			}
			if !valid {
				writeError(response, http.StatusBadRequest, errors.New(message))
				return
			}
			stored = fmt.Sprint(*number)
		}
		if _, err := application.database.ExecContext(request.Context(), `UPDATE settings SET value = ? WHERE key = ?`, stored, name); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
	}
	if input.AvailabilityURLs != nil {
		stored, err := json.Marshal(*input.AvailabilityURLs)
		if err != nil {
			writeError(response, http.StatusBadRequest, errors.New("invalid availability URLs"))
			return
		}
		if _, err := application.database.ExecContext(request.Context(), `UPDATE settings SET value = ? WHERE key = 'availability_urls'`, string(stored)); err != nil {
			writeError(response, http.StatusInternalServerError, err)
			return
		}
	}
	response.WriteHeader(http.StatusNoContent)
}

func (application *Application) readSettings(ctx context.Context) (settingsResponse, error) {
	var result settingsResponse
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'language'`).Scan(&result.Language); err != nil {
		return result, fmt.Errorf("read language: %w", err)
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM system_state WHERE key = 'installation_id'`).Scan(&result.InstallationID); err != nil {
		return result, fmt.Errorf("read installation id: %w", err)
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'scoring_provider'`).Scan(&result.ScoringProvider); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'iplark_threshold'`).Scan(&result.IPLarkThreshold); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'ipcheck_threshold'`).Scan(&result.IPCheckThreshold); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'evaluation_interval_minutes'`).Scan(&result.EvaluationIntervalMinutes); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'history_retention_days'`).Scan(&result.HistoryRetentionDays); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'availability_attempts'`).Scan(&result.AvailabilityAttempts); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'availability_required_successes'`).Scan(&result.AvailabilityRequiredSuccesses); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'availability_timeout_seconds'`).Scan(&result.AvailabilityTimeoutSecs); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'availability_max_latency_ms'`).Scan(&result.AvailabilityMaxLatencyMS); err != nil {
		return result, err
	}
	var urls string
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'availability_urls'`).Scan(&urls); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(urls), &result.AvailabilityURLs); err != nil {
		return result, fmt.Errorf("read availability URLs: %w", err)
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'evaluation_worker_count'`).Scan(&result.EvaluationWorkerCount); err != nil {
		return result, err
	}
	if err := application.database.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'scoring_jitter_ms'`).Scan(&result.ScoringJitterMS); err != nil {
		return result, err
	}
	return result, nil
}

func (application *Application) handlePublishedSubscription(response http.ResponseWriter, request *http.Request) {
	var document []byte
	if err := application.database.QueryRowContext(request.Context(), `SELECT document FROM publications WHERE id = 1`).Scan(&document); err != nil {
		writeError(response, http.StatusServiceUnavailable, errors.New("published subscription unavailable"))
		return
	}
	response.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(document)
}

func loopbackManagementOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/sub/clash.yaml" && request.URL.Path != "/api/health" {
			host, _, err := net.SplitHostPort(request.RemoteAddr)
			if err != nil || !net.ParseIP(strings.Trim(host, "[]")).IsLoopback() {
				http.Error(response, "management interface is loopback-only", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

func ensureParentDirectory(databasePath string) error {
	parent := filepath.Dir(databasePath)
	if parent == "." || parent == "" {
		return nil
	}
	return ensureDirectory(parent)
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]string{"error": err.Error()})
}
