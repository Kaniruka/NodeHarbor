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
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrUnavailable = errors.New("adapter unavailable")

type ProxyNode struct {
	Name   string
	Config map[string]any
}

type ProbeResult struct {
	ExitIdentity string
	Latency      time.Duration
}

type Upstream interface {
	Fetch(context.Context, string) ([]byte, error)
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
	Upstream    Upstream
	Scoring     ScoringProvider
	Kernel      Kernel
	TestChannel TestChannel
}

type Config struct {
	DatabasePath        string
	WebAssets           fs.FS
	EnableTestEndpoints bool
}

type Application struct {
	database     *sql.DB
	handler      http.Handler
	dependencies Dependencies
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
	Language       string `json:"language"`
	InstallationID string `json:"installationId"`
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
	application := &Application{database: database, dependencies: dependencies}
	if err := application.initialize(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	application.handler = application.routes(config)
	return application, nil
}

func (application *Application) Handler() http.Handler { return application.handler }

func (application *Application) Close() error { return application.database.Close() }

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
	installationID, err := randomID()
	if err != nil {
		return fmt.Errorf("generate installation id: %w", err)
	}
	if _, err := application.database.ExecContext(ctx, `INSERT OR IGNORE INTO system_state(key, value) VALUES ('installation_id', ?)`, installationID); err != nil {
		return fmt.Errorf("initialize system state: %w", err)
	}
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
		Language string `json:"language"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, errors.New("invalid settings document"))
		return
	}
	if input.Language != "zh-CN" && input.Language != "en" {
		writeError(response, http.StatusBadRequest, errors.New("language must be zh-CN or en"))
		return
	}
	if _, err := application.database.ExecContext(request.Context(), `UPDATE settings SET value = ? WHERE key = 'language'`, input.Language); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
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
		if request.URL.Path != "/sub/clash.yaml" {
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
