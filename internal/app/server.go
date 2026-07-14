package app

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/version"
)

// pdf.html is the only server-side template kept (for PDF export); all pages are rendered by the React SPA (web/dist).
//
//go:embed templates/pdf.html
var tplFS embed.FS

const cookieName = "rp_session"

type Server struct {
	cfg         *config.Config
	st          *Store
	names       *Names
	pdf         *template.Template
	jobRuns     sync.Map                                                                   // jobID -> *jobRun; shared cancel scope for a job's in-flight runs (ADR 0011)
	itemCancels sync.Map                                                                   // itemID -> context.CancelFunc; per-row cancel of an in-flight run (ADR 0011)
	jobNotify   sync.Map                                                                   // jobID -> bool; opt-in to email the submitter when the job finishes
	buildProv   func(BatchJob, func(runID, convID, taskID string)) (batch.Provider, error) // test seam for the run-item provider; nil → real buildProvider
	schedMu     sync.Mutex                                                                 // serializes scheduleTick (admission + finalize) so ticks can't over-admit or double-finalize (ADR 0004/0011)
	mailFn      func(to []string, subject, htmlBody string) error                          // test seam; nil → real SMTP send
	appTok      *appTokens                                                                 // short-lived scoped tokens for the iframe-app /api/v1 bridge (ADR 0003)
	chatMu      sync.Mutex                                                                 // guards chatLive/chatSeq
	chatLive    map[int64]*chatTurn                                                        // in-flight chat turns; independent ceiling + admin live view (ADR 0012), NOT the run queue
	chatSeq     int64                                                                      // monotonic in-flight chat-turn id
	cleanupMu   sync.Mutex                                                                 // serializes a storage-cleanup pass so the scheduled ticker and a manual "clean now" never overlap (ADR 0017)
}

// statusRecorder records the response status code for use in request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

// logMiddleware logs every request: method, status, latency, and path (static assets are excluded to avoid noise).
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/assets/") || strings.HasPrefix(p, "/app-assets/") || strings.HasPrefix(p, "/site-assets/") || strings.HasPrefix(p, "/api/") ||
			p == "/healthz" || p == "/favicon.svg" || p == "/favicon.ico" || p == "/manifest.webmanifest" ||
			p == "/pwa-icon" || p == "/sw.js" {
			next.ServeHTTP(w, r) // SPA static assets / health checks / API (which have their own concise logs) are skipped here to avoid noise
			return
		}
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		path, _ := url.QueryUnescape(r.URL.RequestURI())
		log.Printf("%-4s %3d %7s  %s", r.Method, sw.status, time.Since(start).Round(time.Millisecond).String(), path)
	})
}

// RunServer loads config, opens the store, bootstraps first-run state, wires the
// HTTP routes, and serves until it errors. The CLI (cmd/report-portal) calls this.
func RunServer(cfgPath string) {
	cfg, err := config.EnsureConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config %s: %v", cfgPath, err)
	}
	if err := os.MkdirAll(config.DirOf(cfg.DBPath), 0o755); err != nil {
		log.Fatal(err)
	}
	st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	if st.CountUsers() == 0 { // first run with no accounts → generate a random admin and print it to the terminal
		pw := randPassword(14)
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
		if err := st.UpsertUser(User{Username: "admin", PasswordHash: string(h), Role: "admin"}); err != nil {
			log.Fatalf("create initial admin: %v", err)
		}
		bar := strings.Repeat("=", 52)
		log.Printf("\n%s\n  first run: created admin account\n    username: admin\n    password: %s\n  log in and change the password in Users soon.\n%s", bar, pw, bar)
	}
	s := &Server{cfg: cfg, st: st, appTok: newAppTokens(30 * time.Minute)}
	s.names = LoadNames(config.DirOf(cfg.DBPath), st)
	s.names.ensureFull() // if the full list is missing, do a best-effort background fetch once
	s.parseTemplates()

	if st.CountTokens() == 0 { // on first run, create a default API token (managed on the System Settings page: multiple tokens / notes / expiry / scope)
		st.CreateToken(randToken(), "default", "all", "")
		log.Printf("default API token created (see Settings page)")
	}

	if len(st.TypeConfigs()) == 0 { // on first run, seed our real report types so the Types page isn't empty
		n := seedDefaultTypes(st)
		log.Printf("seeded %d default report types", n)
	}

	if len(st.KindColors()) == 0 { // on first run, seed the shipped kind→color mapping (admin-editable afterward)
		n := seedDefaultKindColors(st)
		log.Printf("seeded %d default kind colors", n)
	}

	// Bundled Dify adapter (docs/adr/0006-dify-native.md): a marker plugin every Dify
	// target references, so admins configure a workflow by pasting its API key — no
	// manifest import needed.
	if _, ok := st.GetPlugin(difyPluginSlug); !ok {
		st.UpsertPlugin(difyPluginSlug, "Dify Workflow", "1.0.0", "{}", "bundled")
	}

	s.resumeBatchJobs()  // requeue items left in-flight by a restart and relaunch running jobs
	go s.scheduleLoop()  // release one-shot 定时 jobs when their run_at passes (ADR 0007)
	go s.cleanupLoop()   // run the admin-configured storage-retention pass on its cadence (ADR 0017)
	go s.recurringLoop() // fire recurring tasks into the run queue on their cadence (ADR 0018)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// Session-gated, not public: build identity (version/commit) is only shown in the signed-in
	// app footer, so an anonymous scanner can't fingerprint the build against known CVEs.
	mux.HandleFunc("GET /api/version", s.requireUserJSON(s.handleVersion))
	mux.HandleFunc("GET /api/site", s.apiSite)            // public: brand title/logo for login + browser chrome
	mux.HandleFunc("GET /api/openapi.json", s.apiOpenAPI) // public: OpenAPI 3.1 spec for the v1 machine API
	mux.HandleFunc("GET /manifest.webmanifest", s.pwaManifest)
	mux.HandleFunc("GET /pwa-icon", s.pwaIcon)

	// ---- Dify machine API: Bearer token auth (kept unchanged; the workflow depends on it) ----
	mux.HandleFunc("POST /api/reports", s.ingestReport)             // ingest
	mux.HandleFunc("GET /api/reports", s.apiQueryReports)           // query/search historical reports
	mux.HandleFunc("GET /api/reports/manifest", s.apiManifest)      // probe the manifest
	mux.HandleFunc("GET /api/report", s.apiGetReport)               // fetch a single report body (by uid)
	mux.HandleFunc("DELETE /api/report", s.apiDeleteReport)         // retract a report by uid (scope ingest)
	mux.HandleFunc("GET /api/runs", s.apiRuns)                      // report-group view
	mux.HandleFunc("GET /api/symbols", s.apiSymbols)                // stock list / autocomplete (also used by the omnibox)
	mux.HandleFunc("GET /api/tracking", s.apiTracking)              // structured hypotheses / tracking items
	mux.HandleFunc("PATCH /api/tracking/{id}", s.apiTrackingUpdate) // update one tracking item's status (scope ingest)

	// ---- Dify machine API v1: clean contract (JSON errors, portal-derived uid, envelopes) ----
	mux.HandleFunc("POST /api/v1/reports", s.v1Ingest)
	mux.HandleFunc("GET /api/v1/reports", s.v1QueryReports)
	mux.HandleFunc("GET /api/v1/reports/manifest", s.v1Manifest) // more specific than {uid}, matched first
	mux.HandleFunc("GET /api/v1/reports/{uid}", s.v1GetReport)
	mux.HandleFunc("DELETE /api/v1/reports/{uid}", s.v1DeleteReport)
	mux.HandleFunc("GET /api/v1/runs", s.v1Runs)
	mux.HandleFunc("GET /api/v1/symbols", s.v1Symbols)
	mux.HandleFunc("GET /api/v1/tracking", s.v1Tracking)
	mux.HandleFunc("PATCH /api/v1/tracking/{id}", s.v1TrackingUpdate)
	mux.HandleFunc("GET /api/v1/now", s.v1Now) // authoritative clock: UTC instant + panel-tz civil date

	// ---- Browser (React SPA) API: signed-cookie session auth ----
	mux.HandleFunc("GET /api/me", s.apiMe)
	mux.HandleFunc("POST /api/login", s.apiLogin)
	mux.HandleFunc("POST /api/logout", s.apiLogout)
	// Public password reset (no session).
	mux.HandleFunc("POST /api/password/forgot", s.apiForgotPassword)
	mux.HandleFunc("POST /api/password/reset", s.apiResetPassword)
	mux.HandleFunc("GET /api/home", s.requireUserJSON(s.apiHome))
	mux.HandleFunc("GET /api/stock/{symbol}", s.requireUserJSON(s.apiStock))
	mux.HandleFunc("GET /api/run/{key}", s.requireUserJSON(s.apiRun))
	mux.HandleFunc("GET /api/repbody", s.requireUserJSON(s.apiRepBody))

	// ---- Admin API: session + admin ----
	mux.HandleFunc("GET /api/admin/links", s.requireAdminJSON(s.apiAdminLinks))
	mux.HandleFunc("POST /api/admin/links", s.requireAdminJSON(s.apiLinkAdd))
	mux.HandleFunc("PUT /api/admin/links/{id}", s.requireAdminJSON(s.apiLinkEdit))
	mux.HandleFunc("DELETE /api/admin/links/{id}", s.requireAdminJSON(s.apiLinkDelete))
	mux.HandleFunc("POST /api/admin/links/layout", s.requireAdminJSON(s.apiLinkLayout))
	mux.HandleFunc("POST /api/admin/link-groups", s.requireAdminJSON(s.apiLinkGroupAdd))
	mux.HandleFunc("PUT /api/admin/link-groups/{id}", s.requireAdminJSON(s.apiLinkGroupEdit))
	mux.HandleFunc("DELETE /api/admin/link-groups/{id}", s.requireAdminJSON(s.apiLinkGroupDelete))
	mux.HandleFunc("GET /api/admin/types", s.requireAdminJSON(s.apiAdminTypes))
	mux.HandleFunc("POST /api/admin/types/save", s.requireAdminJSON(s.apiTypesSave))
	mux.HandleFunc("POST /api/admin/types/add", s.requireAdminJSON(s.apiTypesAdd))
	mux.HandleFunc("POST /api/admin/types/reorder", s.requireAdminJSON(s.apiTypesReorder))
	mux.HandleFunc("POST /api/admin/types/recompute", s.requireAdminJSON(s.apiTypesRecompute))
	mux.HandleFunc("POST /api/admin/types/restore-defaults", s.requireAdminJSON(s.apiTypesRestoreDefaults))
	mux.HandleFunc("DELETE /api/admin/types/{name}", s.requireAdminJSON(s.apiTypesDelete))
	mux.HandleFunc("POST /api/admin/kind-colors", s.requireAdminJSON(s.apiKindColorSave))
	mux.HandleFunc("GET /api/admin/users", s.requireAdminJSON(s.apiAdminUsers))
	mux.HandleFunc("POST /api/admin/users", s.requireAdminJSON(s.apiUserAdd))
	mux.HandleFunc("POST /api/admin/users/bulk", s.requireAdminJSON(s.apiUsersBulk))
	mux.HandleFunc("PUT /api/admin/users/{name}", s.requireAdminJSON(s.apiUserSave))
	mux.HandleFunc("DELETE /api/admin/users/{name}", s.requireAdminJSON(s.apiUserDelete))
	// Organizational user groups (labels; permissions still come from the role).
	mux.HandleFunc("GET /api/admin/groups", s.requireAdminJSON(s.apiAdminGroups))
	mux.HandleFunc("POST /api/admin/groups", s.requireAdminJSON(s.apiGroupAdd))
	mux.HandleFunc("PUT /api/admin/groups/{id}", s.requireAdminJSON(s.apiGroupSave))
	mux.HandleFunc("DELETE /api/admin/groups/{id}", s.requireAdminJSON(s.apiGroupDelete))
	mux.HandleFunc("GET /api/admin/settings", s.requireAdminJSON(s.apiAdminSettings))
	mux.HandleFunc("POST /api/admin/settings", s.requireAdminJSON(s.apiSettingsSave))
	mux.HandleFunc("GET /api/admin/email", s.requireAdminJSON(s.apiEmailGet))
	mux.HandleFunc("POST /api/admin/email", s.requireAdminJSON(s.apiEmailSave))
	mux.HandleFunc("POST /api/admin/email/test", s.requireAdminJSON(s.apiEmailTest))
	mux.HandleFunc("POST /api/admin/site-asset", s.requireAdminJSON(s.apiSiteAssetUpload))
	mux.HandleFunc("GET /api/admin/tokens", s.requireAdminJSON(s.apiAdminTokens))
	mux.HandleFunc("POST /api/admin/tokens", s.requireAdminJSON(s.apiTokenAdd))
	mux.HandleFunc("DELETE /api/admin/tokens/{id}", s.requireAdminJSON(s.apiTokenDelete))

	// ---- Batch-run feature (see docs/adr/0001-batch-run-engine.md) ----
	// Executor manifests + targets + config are admin-only (PermManage); running jobs is PermRunBatch.
	mux.HandleFunc("GET /api/admin/batch/plugins", s.requireAdminJSON(s.apiBatchPlugins))
	mux.HandleFunc("POST /api/admin/batch/plugins/import", s.requireAdminJSON(s.apiBatchPluginImport))
	mux.HandleFunc("DELETE /api/admin/batch/plugins/{slug}", s.requireAdminJSON(s.apiBatchPluginDelete))
	mux.HandleFunc("GET /api/admin/batch/config", s.requireAdminJSON(s.apiBatchConfigGet))
	mux.HandleFunc("POST /api/admin/batch/config", s.requireAdminJSON(s.apiBatchConfigSave))
	mux.HandleFunc("GET /api/admin/batch/targets", s.requirePermJSON(PermRunBatch, s.apiBatchTargets))
	mux.HandleFunc("POST /api/admin/batch/targets", s.requireAdminJSON(s.apiBatchTargetAdd))
	mux.HandleFunc("POST /api/admin/batch/targets/reorder", s.requireAdminJSON(s.apiBatchTargetReorder))
	// Dify-native config (docs/adr/0006-dify-native.md): probe a workflow by key, then save it.
	mux.HandleFunc("POST /api/admin/batch/dify/probe", s.requireAdminJSON(s.apiBatchDifyProbe))
	mux.HandleFunc("POST /api/admin/batch/dify/targets", s.requireAdminJSON(s.apiBatchDifyTargetAdd))
	mux.HandleFunc("GET /api/admin/batch/dify/targets/{id}", s.requireAdminJSON(s.apiBatchDifyTargetGet))
	mux.HandleFunc("PUT /api/admin/batch/dify/targets/{id}", s.requireAdminJSON(s.apiBatchDifyTargetUpdate))
	mux.HandleFunc("DELETE /api/admin/batch/targets/{id}", s.requireAdminJSON(s.apiBatchTargetDelete))
	mux.HandleFunc("GET /api/admin/batch/tickets", s.requirePermJSON(PermRunBatch, s.apiBatchTickets))
	mux.HandleFunc("GET /api/admin/batch/queue", s.requirePermJSON(PermRunBatch, s.apiBatchQueue))
	mux.HandleFunc("GET /api/admin/batch/jobs", s.requirePermJSON(PermRunBatch, s.apiBatchJobs))
	mux.HandleFunc("POST /api/admin/batch/jobs", s.requirePermJSON(PermRunBatch, s.apiBatchJobCreate))
	mux.HandleFunc("GET /api/admin/batch/jobs/{id}", s.requirePermJSON(PermRunBatch, s.apiBatchJobDetail))
	// Queue mutations are admin-only, except cancel — a non-admin may cancel their own
	// run (ownership is checked inside apiBatchJobCancel).
	mux.HandleFunc("POST /api/admin/batch/jobs/clear-finished", s.requireAdminJSON(s.apiBatchClearFinished))
	mux.HandleFunc("DELETE /api/admin/batch/jobs/{id}", s.requireAdminJSON(s.apiBatchJobDelete))
	mux.HandleFunc("POST /api/admin/batch/jobs/{id}/cancel", s.requirePermJSON(PermRunBatch, s.apiBatchJobCancel))
	mux.HandleFunc("POST /api/admin/batch/jobs/{id}/items/cancel", s.requirePermJSON(PermRunBatch, s.apiBatchItemsCancel))
	mux.HandleFunc("POST /api/admin/batch/items/{id}/reconcile", s.requireAdminJSON(s.apiBatchItemReconcile))
	mux.HandleFunc("POST /api/admin/batch/jobs/{id}/retry", s.requireAdminJSON(s.apiBatchJobRetry))
	mux.HandleFunc("POST /api/admin/batch/jobs/{id}/priority", s.requireAdminJSON(s.apiBatchJobReprioritize))
	mux.HandleFunc("POST /api/admin/batch/jobs/{id}/schedule", s.requireAdminJSON(s.apiBatchJobSchedule))
	// Preset low-peak scheduling windows (ADR 0014): the run form reads the list (PermRunBatch);
	// admins manage them.
	mux.HandleFunc("GET /api/admin/batch/presets", s.requirePermJSON(PermRunBatch, s.apiRunPresets))
	mux.HandleFunc("POST /api/admin/batch/presets", s.requireAdminJSON(s.apiRunPresetCreate))
	mux.HandleFunc("POST /api/admin/batch/presets/reorder", s.requireAdminJSON(s.apiRunPresetReorder))
	mux.HandleFunc("PUT /api/admin/batch/presets/{id}", s.requireAdminJSON(s.apiRunPresetUpdate))
	mux.HandleFunc("DELETE /api/admin/batch/presets/{id}", s.requireAdminJSON(s.apiRunPresetDelete))

	// ---- Recurring tasks (scheduled tasks; docs/adr/0018-recurring-tasks.md) ----
	// PermRunBatch (operators schedule their own recurring runs, not admin-only); every handler
	// checks ownership in-line (a non-admin sees/edits only their own tasks, an admin all).
	mux.HandleFunc("GET /api/admin/batch/recurring", s.requirePermJSON(PermRunBatch, s.apiRecurringList))
	mux.HandleFunc("POST /api/admin/batch/recurring", s.requirePermJSON(PermRunBatch, s.apiRecurringCreate))
	mux.HandleFunc("GET /api/admin/batch/recurring/{id}", s.requirePermJSON(PermRunBatch, s.apiRecurringDetail))
	mux.HandleFunc("PUT /api/admin/batch/recurring/{id}", s.requirePermJSON(PermRunBatch, s.apiRecurringUpdate))
	mux.HandleFunc("POST /api/admin/batch/recurring/{id}/enable", s.requirePermJSON(PermRunBatch, s.apiRecurringEnable))
	mux.HandleFunc("POST /api/admin/batch/recurring/{id}/run", s.requirePermJSON(PermRunBatch, s.apiRecurringRunNow))
	mux.HandleFunc("DELETE /api/admin/batch/recurring/{id}", s.requirePermJSON(PermRunBatch, s.apiRecurringDelete))

	// ---- Storage cleanup console (docs/adr/0017-storage-cleanup.md): admin-only (PermManage) ----
	mux.HandleFunc("GET /api/admin/cleanup/config", s.requireAdminJSON(s.apiCleanupConfigGet))
	mux.HandleFunc("POST /api/admin/cleanup/config", s.requireAdminJSON(s.apiCleanupConfigSave))
	mux.HandleFunc("GET /api/admin/cleanup/usage", s.requireAdminJSON(s.apiCleanupUsage))
	mux.HandleFunc("POST /api/admin/cleanup/preview", s.requireAdminJSON(s.apiCleanupPreview))
	mux.HandleFunc("POST /api/admin/cleanup/run", s.requireAdminJSON(s.apiCleanupRunNow))
	mux.HandleFunc("GET /api/admin/cleanup/history", s.requireAdminJSON(s.apiCleanupHistory))

	// ---- Interactive chat / assistant (docs/adr/0012-interactive-chat.md) ----
	// Cookie session, gated by PermRunBatch (a chat turn runs a Dify app). Conversations
	// are personal — each handler is scoped to the caller's own rows.
	mux.HandleFunc("GET /api/chat/config", s.requirePermJSON(PermRunBatch, s.apiChatConfig))
	mux.HandleFunc("GET /api/chat/targets", s.requirePermJSON(PermRunBatch, s.apiChatTargets))
	mux.HandleFunc("GET /api/chat/targets/{id}/intro", s.requirePermJSON(PermRunBatch, s.apiChatTargetIntro))
	mux.HandleFunc("GET /api/chat/conversations", s.requirePermJSON(PermRunBatch, s.apiChatConversations))
	mux.HandleFunc("POST /api/chat/conversations", s.requirePermJSON(PermRunBatch, s.apiChatConversationCreate))
	mux.HandleFunc("DELETE /api/chat/conversations/{id}", s.requirePermJSON(PermRunBatch, s.apiChatConversationDelete))
	mux.HandleFunc("POST /api/chat/conversations/{id}/rename", s.requirePermJSON(PermRunBatch, s.apiChatConversationRename))
	mux.HandleFunc("POST /api/chat/conversations/{id}/star", s.requirePermJSON(PermRunBatch, s.apiChatConversationStar))
	mux.HandleFunc("GET /api/chat/conversations/{id}/messages", s.requirePermJSON(PermRunBatch, s.apiChatHistory))
	mux.HandleFunc("POST /api/chat/conversations/{id}/messages", s.requirePermJSON(PermRunBatch, s.apiChatSend))
	mux.HandleFunc("POST /api/chat/conversations/{id}/messages/stream", s.requirePermJSON(PermRunBatch, s.apiChatSendStream))
	mux.HandleFunc("GET /api/chat/conversations/{id}/outcome", s.requirePermJSON(PermRunBatch, s.apiChatOutcome))
	mux.HandleFunc("POST /api/chat/conversations/{id}/stop", s.requirePermJSON(PermRunBatch, s.apiChatStop)) // owner stops their in-flight turn
	// Assistant admin: the concurrency ceiling, the live "who is chatting now" view, and read-only
	// oversight of any user's conversations + messages (ADR 0012).
	mux.HandleFunc("GET /api/admin/chat/conversations", s.requireAdminJSON(s.apiAdminChatConversations))
	mux.HandleFunc("GET /api/admin/chat/conversations/{id}/messages", s.requireAdminJSON(s.apiAdminChatHistory))
	mux.HandleFunc("GET /api/admin/chat/live", s.requireAdminJSON(s.apiAdminChatLive))
	mux.HandleFunc("POST /api/admin/chat/stop/{id}", s.requireAdminJSON(s.apiAdminChatStop)) // admin stops any in-flight turn
	mux.HandleFunc("POST /api/admin/chat/config", s.requireAdminJSON(s.apiAdminChatConfigSave))

	// ---- Downloadable iframe apps (see docs/adr/0003-downloadable-apps.md) ----
	// List/open is any-user; install/uninstall is admin; assets are served publicly.
	mux.HandleFunc("GET /api/apps", s.requireUserJSON(s.apiApps))
	mux.HandleFunc("POST /api/apps/{id}/token", s.requireUserJSON(s.apiAppToken))
	mux.HandleFunc("POST /api/admin/apps/install", s.requireAdminJSON(s.apiAppInstall))
	mux.HandleFunc("GET /api/admin/apps/market", s.requireAdminJSON(s.apiAppMarket))
	mux.HandleFunc("POST /api/admin/apps/market/install", s.requireAdminJSON(s.apiAppMarketInstall))
	mux.HandleFunc("DELETE /api/admin/apps/{id}", s.requireAdminJSON(s.apiAppDelete))
	mux.HandleFunc("GET /app-assets/{id}/{path...}", s.appAssets)
	mux.HandleFunc("GET /site-assets/{name}", s.siteAsset)

	// ---- Outbound event webhooks (extension point; see docs/adr/0002-extension-architecture.md) ----
	mux.HandleFunc("GET /api/admin/webhooks", s.requireAdminJSON(s.apiWebhooks))
	mux.HandleFunc("POST /api/admin/webhooks", s.requireAdminJSON(s.apiWebhookAdd))
	mux.HandleFunc("DELETE /api/admin/webhooks/{id}", s.requireAdminJSON(s.apiWebhookDelete))
	mux.HandleFunc("POST /api/admin/webhooks/{id}/test", s.requireAdminJSON(s.apiWebhookTest))

	// ---- Downloads: MD / PDF (cookie session) ----
	mux.HandleFunc("GET /report/{rid}/md", s.requireUser(s.reportMD))
	mux.HandleFunc("GET /report/{rid}/pdf", s.requireUser(s.reportPDF))
	mux.HandleFunc("GET /report/day.zip", s.requireUser(s.reportDayZip)) // every report a stock has on one date, as a ZIP of .md + .pdf

	// ---- SPA: hand all other paths to React (deep links fall back to index.html) ----
	mux.HandleFunc("GET /", s.spaHandler())

	log.Printf("report-portal %s | listen %s | db %s | reports:%d", version.String(), cfg.Listen, cfg.DBDriver, st.CountNew())
	if err := http.ListenAndServe(cfg.Listen, logMiddleware(gzipMiddleware(mux))); err != nil {
		log.Fatal(err)
	}
}

// handleHealthz is the public, unauthenticated liveness probe. It returns nothing but liveness —
// no data counts (business volume) and no build identity (version/commit), both of which would
// help an anonymous scanner fingerprint the instance. Ops read the build from /api/version (which
// requires a session) or the server logs.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

// handleVersion returns build identity for the signed-in app footer. It is session-gated
// (registered behind requireUserJSON) precisely so version/commit are NOT exposed to anonymous
// callers: commit especially pins the exact public source, making CVE fingerprinting trivial.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"version": version.Version, "commit": version.Commit, "buildDate": version.BuildDate})
}

// AddUser creates or updates an account from the CLI (lockout fallback).
func AddUser(cfgPath, name, pw string, admin bool) error {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	os.MkdirAll(config.DirOf(c.DBPath), 0o755)
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return err
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
	role := "user"
	if admin {
		role = "admin"
	}
	return st.UpsertUser(User{Username: name, PasswordHash: string(h), Role: role})
}

// RecomputeKinds opens the store and re-derives every report's top-level kind with
// the current taxonomy rules (returns rows updated). Behind the `recompute-kinds` CLI.
func RecomputeKinds(cfgPath string) (int, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, err
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.RecomputeKinds()
}

// FreezeReportNames snapshots the current name onto every un-named report row so a later
// rename never rewrites history (run once after backfilling stock names; idempotent).
func FreezeReportNames(cfgPath string) (int64, error) {
	c, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, err
	}
	st, err := OpenStore(c.DBDriver, c.DBSource())
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.FreezeReportNames()
}

// FetchNames fetches the full A-share name list to <data-dir>/names.json and
// returns the count and the written path.
func FetchNames(cfgPath string) (int, string, error) {
	dir := "data"
	if c, err := config.EnsureConfig(cfgPath); err == nil {
		dir = config.DirOf(c.DBPath)
	}
	n, err := FetchNamesToFile(dir)
	return n, dir + "/names.json", err
}

// randPassword generates a random password (excluding easily confused characters 0/O/1/l/I).
func randPassword(n int) string {
	const cs = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = cs[i%len(cs)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = cs[int(b[i])%len(cs)]
	}
	return string(b)
}

// randToken generates an ingest API token (48 hex digits).
func randToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- Templates ----------

// parseTemplates parses only the PDF export template (pages are handled by the React SPA).
func (s *Server) parseTemplates() {
	funcs := template.FuncMap{
		"safe": func(s string) template.HTML { return template.HTML(s) },
		"trunc10": func(s string) string {
			if len(s) >= 10 {
				return s[:10]
			}
			return s
		},
	}
	s.pdf = template.Must(template.New("pdf.html").Funcs(funcs).ParseFS(tplFS, "templates/pdf.html"))
}

// ---------- Session / auth ----------

func (s *Server) sign(user string) string {
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()
	msg := fmt.Sprintf("%s|%d", user, exp)
	sig := s.hmac(msg)
	return base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." + sig
}

func (s *Server) hmac(msg string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) verify(cookie string) string {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	msg := string(raw)
	if !hmac.Equal([]byte(s.hmac(msg)), []byte(parts[1])) {
		return ""
	}
	i := strings.LastIndex(msg, "|")
	if i < 0 {
		return ""
	}
	exp, err := strconv.ParseInt(msg[i+1:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	return msg[:i]
}

func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return s.verify(c.Value)
}

// currentActiveUser returns the logged-in user only if the account still exists and
// is enabled, so disabling an account takes effect immediately — even for a session
// whose cookie is still valid.
func (s *Server) currentActiveUser(r *http.Request) string {
	u := s.currentUser(r)
	if u == "" {
		return ""
	}
	if usr := s.st.GetUser(u); usr == nil || !usr.Active {
		return ""
	}
	return u
}

func (s *Server) isAdmin(user string) bool {
	u := s.st.GetUser(user)
	return u != nil && can(u.Role, PermManage)
}

// hasPerm reports whether the logged-in user's role holds a permission.
func (s *Server) hasPerm(user, perm string) bool {
	u := s.st.GetUser(user)
	return u != nil && can(u.EffRole(), perm)
}

type handler func(http.ResponseWriter, *http.Request, string)

func (s *Server) requireUser(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.currentActiveUser(r)
		if u == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, u)
	}
}

// ---------- Login ----------

// ---------- List ----------

func (s *Server) filtersFrom(r *http.Request) (Filters, string, int, int) {
	q := r.URL.Query()
	f := Filters{
		Q: strings.TrimSpace(q.Get("q")), Scope: q.Get("scope"), Symbol: q.Get("symbol"),
		RType: q.Get("rtype"), Kind: q.Get("kind"), DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"),
		Sort: q.Get("sort"),
	}
	src := q.Get("src")
	if src == "" {
		src = "all"
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size != 15 && size != 30 && size != 50 {
		size = 30
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	return f, src, size, page
}

// ---------- run detail ----------

func (s *Server) runMembers(key string) []Rep {
	var members []Rep
	if !strings.Contains(key, "|") {
		if rep := s.loadRep(key); rep != nil {
			members = []Rep{*rep}
		}
	} else {
		parts := strings.SplitN(key, "|", 2)
		symbol, date := parts[0], parts[1]
		nn, _ := s.st.SearchNew(Filters{DateFrom: date, DateTo: date, Sort: "date_asc"})
		for _, m := range nn {
			if m.Symbol == symbol {
				members = append(members, m)
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return members[i].Time < members[j].Time })
	}
	for i := range members {
		members[i].Label = label(members[i])
	}
	return members
}

// orderAndDefault sorts members and picks the default page (the type marked "汇总") according to the type config (which admins can edit).
// Fallback when unconfigured: detect summary by keyword → otherwise the last item. Tab labels can be renamed via config.
// defaultTypeOrd: the built-in default tab order for unconfigured types (conclusion first, the rest following the analysis flow).
// Dragging in "Type Management" writes to type_config.ord, which takes precedence over this.
var defaultTypeOrd = map[string]int{
	"投资决策建议": 0, "综合深度研究": 0,
	"事件监测": 10, "投资机会": 10, "研报分析": 10,
	"舆情分析": 20, "重组基本面分析": 20,
	"行业分析": 30, "重组分析": 30,
	"财务分析": 40, "资本运作分析": 40,
	"估值分析":   50,
	"股权分析":   60,
	"管理能力分析": 70,
	"调研纪要":   80,
}

// defaultSeedTypes is the set of report types pre-registered on a fresh DB so the
// Type Management page ships with our real categories instead of being empty.
// Admins can rename/reassign category/reorder/add in the UI afterward.
//
// These are the actual categories our Dify workflow modules emit (dept-1 single-
// stock analysis → 投资决策/深度研究; the 3-x 重组 series → 重组决策; DeepResearch →
// 深度研究), cross-checked against the categories present in ingested data.
var defaultSeedTypes = []struct {
	Name    string
	Kind    string
	Ord     int
	Summary bool
}{
	// 投资决策 (dept-1 single-stock analysis; 投资决策建议 is the decision summary).
	// 舆情分析 (dept-1 1-2) and 管理能力分析 (dept-1 1-5) are investment inputs, not 重组/深度研究.
	{"汇总", "投资决策", 0, true},
	{"投资决策建议", "投资决策", 10, true},
	{"研报分析", "投资决策", 20, false},
	{"行业分析", "投资决策", 30, false},
	{"舆情分析", "投资决策", 35, false},
	{"估值分析", "投资决策", 40, false},
	{"财务分析", "投资决策", 50, false},
	{"股权分析", "投资决策", 60, false},
	{"管理能力分析", "投资决策", 65, false},
	{"投资机会", "投资决策", 70, false},
	// 深度研究 (DeepResearch-DS emits 综合深度研究 / 重组深度研究 for single-stock queries,
	// and 专题研究 for non-single-stock queries — macro/industry/strategy/M&A/multi-company
	// comparison, identified by title instead of a stock symbol; 调研纪要 is manual)
	{"综合深度研究", "深度研究", 0, true},
	{"重组深度研究", "深度研究", 10, false},
	{"专题研究", "深度研究", 15, false},
	{"调研纪要", "深度研究", 20, false},
	// 重组决策 (the 3-x 重组 series; 综合决策 is the 3-5 summary; the rest are its sub-models)
	{"综合决策", "重组决策", 0, true},
	{"重组基本面分析", "重组决策", 10, false},
	{"交易分析", "重组决策", 20, false},
	{"重组舆情分析", "重组决策", 30, false},
	{"资本运作分析", "重组决策", 40, false},
	{"事件监测", "重组决策", 50, false},
	{"信号监测", "重组决策", 60, false},
	// 技术分析 (Daily_Quote 缠论 / 技术分析)
	{"技术分析", "技术分析", 0, false},
	{"缠论分析", "技术分析", 10, false},
	// 未分类 (uncategorized / thematic — its own bucket)
	{"未分类", "未分类", 0, false},
}

// seedDefaultTypes registers the default report types (first run only) and returns the count.
func seedDefaultTypes(st *Store) int {
	for _, t := range defaultSeedTypes {
		st.UpsertTypeConfig(t.Name, t.Kind, "", t.Ord, t.Summary)
	}
	return len(defaultSeedTypes)
}

// defaultKindColors is the shipped kind→antd-Tag-color mapping (first run only),
// matching the pipeline kinds in kindOrder. Admins can change any of these
// afterward on the Types Management page.
var defaultKindColors = []struct {
	Kind  string
	Color string
}{
	{"重组决策", "volcano"},
	{"投资决策", "green"},
	{"深度研究", "geekblue"},
	{"技术分析", "purple"},
	{"事件监测", "gold"},
}

// seedDefaultKindColors registers the default kind colors (first run only) and returns the count.
func seedDefaultKindColors(st *Store) int {
	for _, c := range defaultKindColors {
		st.SetKindColor(c.Kind, c.Color)
	}
	return len(defaultKindColors)
}

func (s *Server) orderAndDefault(members []Rep) ([]Rep, string) {
	cfg := s.st.TypeConfigs()
	ord := func(r Rep) int {
		if c, ok := cfg[r.RType]; ok {
			return c.Ord
		}
		if o, ok := defaultTypeOrd[r.RType]; ok {
			return o
		}
		return 1000
	}
	sum := func(r Rep) bool {
		if c, ok := cfg[r.RType]; ok && c.IsSummary {
			return true
		}
		return isSummary(r)
	}
	out := make([]Rep, len(members))
	copy(out, members)
	for i := range out {
		if c, ok := cfg[out[i].RType]; ok && c.Label != "" {
			out[i].Label = c.Label
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := sum(out[i]), sum(out[j]); si != sj {
			return si // summary / comprehensive / decision come first
		}
		if oi, oj := ord(out[i]), ord(out[j]); oi != oj {
			return oi < oj
		}
		return out[i].Time < out[j].Time
	})
	// append an index to same-named tabs so multiple "重组交易分析" entries can be told apart
	seen := map[string]int{}
	for i := range out {
		seen[out[i].Label]++
		if n := seen[out[i].Label]; n > 1 {
			out[i].Label = out[i].Label + " " + strconv.Itoa(n)
		}
	}
	def, bestOrd := "", 1<<30
	for _, m := range out {
		if c, ok := cfg[m.RType]; ok && c.IsSummary && c.Ord < bestOrd {
			bestOrd, def = c.Ord, m.RID
		}
	}
	if def == "" {
		for _, m := range out {
			if isSummary(m) {
				def = m.RID
				break
			}
		}
	}
	if def == "" && len(out) > 0 {
		def = out[len(out)-1].RID
	}
	return out, def
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func containsStr(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

// repKind returns a report's top-level category: new reports use the Kind field, otherwise it is inferred from the type.
func repKind(r Rep) string {
	if r.Kind != "" {
		return foldKind(r.Kind)
	}
	return runKind([]string{r.RType})
}

// tokenOK validates the Bearer token in the request; need = the required scope (ingest|query), and a token with scope=all passes everything.
// Besides persistent api_tokens it also accepts an ephemeral, scoped app-bridge
// token (ADR 0003) — these are query-only, so ingest paths still fall through to
// the DB check and reject them.
func (s *Server) tokenOK(r *http.Request, need string) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if s.appTok != nil && s.appTok.valid(got, need, time.Now()) {
		return true
	}
	return s.st.TokenValid(got, need)
}

// ingestReport is the ingest endpoint for new reports (called by the Dify workflow's HTTP node). Bearer token auth.
func (s *Server) ingestReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		UID      string `json:"uid"`
		RunID    string `json:"run_id"`
		Symbol   string `json:"symbol"`
		Name     string `json:"name"` // optional: as-of company name; snapshotted so backdoor-listing/rename doesn't relabel old reports
		Date     string `json:"date"`
		Kind     string `json:"kind"`
		Subtype  string `json:"subtype"`
		RType    string `json:"rtype"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Time     string `json:"time"`
		BodyMD   string `json:"body_md"`
		BodyHTML string `json:"body_html"`
		Tracking []struct {
			IType       string `json:"itype"`
			Content     string `json:"content"`
			Status      string `json:"status"`
			ReviewPoint string `json:"review_point"`
		} `json:"tracking"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Date == "" || (in.Symbol == "" && in.Title == "") {
		http.Error(w, "date and (symbol or title) required", http.StatusBadRequest)
		return
	}
	rtype := firstNonEmpty(in.Subtype, in.RType)
	// Top-level category: explicit from Dify > already registered in the registry > runKind fallback; and auto-register this subtype → category.
	kind := in.Kind
	if kind == "" {
		kind = s.st.TypeKind(rtype)
	}
	if kind == "" {
		kind = runKind([]string{rtype})
	}
	s.st.RegisterType(rtype, kind)
	s.names.EnsureOne(in.Symbol) // if this code has no name yet, do a background best-effort fetch from Tencent/Sina
	// Identity key = symbol|date|category|subtype: the same type coming in again on the same day overwrites/updates it (run_id is just a batch label and does not participate in identity).
	// Thematic reports (no single home stock) have no symbol — fall back to the title so
	// different topics on the same day don't collide into one uid.
	uid := firstNonEmpty(in.UID, firstNonEmpty(in.Symbol, in.Title)+"|"+in.Date+"|"+kind+"|"+rtype)
	// Snapshot the as-of name: Dify-provided > current stocks name at ingest time.
	// The report is immutable history, so a later rename/backdoor-listing won't relabel it.
	name := firstNonEmpty(in.Name, s.names.Get(in.Symbol))
	rep := Rep{
		UID: uid, RunID: in.RunID, Symbol: in.Symbol, Name: name, Date: in.Date, Kind: kind,
		RType: rtype, Title: in.Title, Source: in.Source, Time: firstNonEmpty(in.Time, in.Date),
		MD: in.BodyMD, HTML: htmlToStore(in.BodyMD, in.BodyHTML),
	}
	created, err := s.st.UpsertReport(rep)
	if err != nil {
		log.Printf("ingest db error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if len(in.Tracking) > 0 { // only update when tracking items are present (overwrite the old ones to match the latest body)
		items := make([]TrackingItem, 0, len(in.Tracking))
		for _, t := range in.Tracking {
			items = append(items, TrackingItem{IType: t.IType, Content: t.Content, Status: t.Status, ReviewPoint: t.ReviewPoint})
		}
		s.st.SetTracking(uid, in.Symbol, items)
	}
	log.Printf("ingest %s %s created=%v", in.Symbol, in.Date, created)
	s.fireEvent(EventReportIngested, map[string]any{
		"uid": uid, "symbol": in.Symbol, "name": name, "date": in.Date,
		"rtype": rtype, "kind": kind, "title": in.Title, "source": in.Source, "created": created,
	})
	writeJSON(w, map[string]any{"ok": true, "uid": uid, "created": created})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// apiQueryReports queries/searches historical reports (used by Dify to re-check hypotheses / tracking items). Bearer token auth, scope query.
// GET /api/reports?symbol=300750&q=关键字&kind=投资决策&subtype=汇总&since=&until=&limit=20&with_body=1
// At least one of symbol and q must be given; when symbol is empty, search the whole database by keyword.
func (s *Server) apiQueryReports(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	kw := strings.TrimSpace(q.Get("q"))
	runID := strings.TrimSpace(q.Get("run_id"))
	if symbol == "" && kw == "" && runID == "" {
		http.Error(w, "symbol, q or run_id required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	withBody := q.Get("with_body") == "1" || q.Get("with_body") == "true"
	since, until := q.Get("since"), q.Get("until")
	if d := strings.TrimSpace(q.Get("date")); d != "" { // date (an exact day; "today" is an alias)
		if d == "today" {
			d = time.Now().Format("2006-01-02")
		}
		since, until = d, d
	}
	reps, total, err := s.st.QueryReports(ReportQuery{
		Symbol: symbol, Q: kw, Kind: q.Get("kind"), RType: firstNonEmpty(q.Get("subtype"), q.Get("rtype")),
		Source: strings.TrimSpace(q.Get("source")), RunID: runID,
		Since: since, Until: until, Limit: limit, Offset: offset, WithBody: withBody,
	})
	if err != nil {
		log.Printf("query db error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"symbol": symbol, "q": kw, "has": total > 0,
		"count": len(reps), "total": total, "offset": offset, "limit": limit, "reports": s.repsJSON(reps, withBody)})
	log.Printf("query symbol=%s -> %d/%d reports", symbol, len(reps), total)
}

// repsJSON converts []Rep into Dify-friendly JSON (includes the company name; includes body_md when withBody is set).
func (s *Server) repsJSON(reps []Rep, withBody bool) []map[string]any {
	out := make([]map[string]any, 0, len(reps))
	for _, r := range reps {
		m := map[string]any{"uid": r.UID, "run_id": r.RunID, "symbol": r.Symbol, "name": s.names.Get(r.Symbol),
			"date": r.Date, "kind": r.Kind, "subtype": r.RType, "title": r.Title, "source": r.Source}
		if withBody {
			m["body_md"] = r.MD
		}
		out = append(out, m)
	}
	return out
}

// apiManifest lists "what reports exist" for a symbol: total count, each date (with category), and all categories/subtypes. Lets Dify probe before fetching.
// GET /api/reports/manifest?symbol=300750
func (s *Server) apiManifest(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	m := s.st.Manifest(symbol)
	m["name"] = s.names.Get(symbol)
	writeJSON(w, m)
	log.Printf("manifest %s -> %v reports", symbol, m["total"])
}

// apiGetReport fetches a single report body. GET /api/report?uid=... (or rid=n123). Bearer token auth, scope query.
func (s *Server) apiGetReport(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	var rep *Rep
	if uid := strings.TrimSpace(q.Get("uid")); uid != "" {
		rep = s.st.GetByUID(uid)
	} else if rid := strings.TrimSpace(q.Get("rid")); rid != "" {
		rep = s.loadRep(rid)
	}
	if rep == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"uid": rep.UID, "run_id": rep.RunID, "symbol": rep.Symbol, "date": rep.Date,
		"kind": rep.Kind, "subtype": rep.RType, "title": rep.Title, "source": rep.Source,
		"body_md": rep.MD, "body_html": htmlOf(*rep)})
}

// apiDeleteReport retracts a report and its tracking items by uid. Bearer scope ingest. DELETE /api/report?uid=...
func (s *Server) apiDeleteReport(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	uid := strings.TrimSpace(r.URL.Query().Get("uid"))
	if uid == "" {
		http.Error(w, "uid required", http.StatusBadRequest)
		return
	}
	n, err := s.st.DeleteReport(uid)
	if err != nil {
		log.Printf("delete db error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	log.Printf("delete %s -> %d", uid, n)
	writeJSON(w, map[string]any{"ok": true, "deleted": n}) // deleted:0 (not 404) so retries stay idempotent
}

// apiTrackingUpdate updates one tracking item's status/review_point by id (the hypothesis re-check loop).
// Bearer scope ingest. PATCH /api/tracking/{id} with body {status?, review_point?}.
func (s *Server) apiTrackingUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r, "ingest") {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var in struct {
		Status      string `json:"status"`
		ReviewPoint string `json:"review_point"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in) // empty body tolerated
	if strings.TrimSpace(in.Status) == "" && strings.TrimSpace(in.ReviewPoint) == "" {
		http.Error(w, "status or review_point required", http.StatusBadRequest)
		return
	}
	ok, err := s.st.UpdateTrackingStatus(id, strings.TrimSpace(in.Status), strings.TrimSpace(in.ReviewPoint))
	if err != nil {
		log.Printf("tracking update db error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "status": in.Status})
}

// apiRuns is the report-group view (one generation run = same symbol + date + category). GET /api/runs?symbol=300750&date= (optional)
func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	symbol := strings.TrimSpace(r.URL.Query().Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	runs := s.st.ListRuns(symbol, strings.TrimSpace(r.URL.Query().Get("date")))
	out := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		out = append(out, map[string]any{"symbol": r.Symbol, "date": r.Date, "kind": r.Kind,
			"run_id": r.RunID, "subtypes": r.Subtypes, "count": r.Count})
	}
	writeJSON(w, map[string]any{"symbol": symbol, "name": s.names.Get(symbol), "has": len(out) > 0, "count": len(out), "runs": out})
}

// apiSymbols lists stocks that have reports / autocomplete. GET /api/symbols?q=300&limit=50
func (s *Server) apiSymbols(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	list := s.st.ListSymbols(strings.TrimSpace(q.Get("q")), limit)
	out := make([]map[string]any, 0, len(list))
	for _, si := range list {
		name := si.Name // name from the DB (stocks); fall back to the in-memory map if absent
		if name == "" {
			name = s.names.Get(si.Symbol)
		}
		out = append(out, map[string]any{"symbol": si.Symbol, "name": name, "count": si.Count, "latest": si.Latest})
	}
	writeJSON(w, map[string]any{"count": len(out), "symbols": out})
}

// apiTracking returns structured hypotheses / tracking items (for re-run review). GET /api/tracking?symbol=300750&status=pending&limit=100
func (s *Server) apiTracking(w http.ResponseWriter, r *http.Request) {
	if !s.canQuery(r) { // Bearer(query) or a logged-in browser session
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	if symbol == "" {
		http.Error(w, "symbol required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items := s.st.QueryTracking(symbol, strings.TrimSpace(q.Get("status")), limit)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"id": it.ID, "report_uid": it.ReportUID, "itype": it.IType, "content": it.Content,
			"status": it.Status, "review_point": it.ReviewPoint, "created_at": it.Created})
	}
	writeJSON(w, map[string]any{"symbol": symbol, "has": len(out) > 0, "count": len(out), "items": out})
}

func repInList(reps []Rep, rid string) bool {
	for _, r := range reps {
		if r.RID == rid {
			return true
		}
	}
	return false
}

// loadRep fetches a report including its body by rid.
func (s *Server) loadRep(rid string) *Rep {
	if strings.HasPrefix(rid, "n") {
		id, err := strconv.ParseInt(rid[1:], 10, 64)
		if err != nil {
			return nil
		}
		rep, _ := s.st.GetNew(id)
		return rep
	}
	return nil
}

// ---------- Export ----------

func (s *Server) reportMD(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r.PathValue("rid"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	fn := safeFile(rep.Title, rid(r)) + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write([]byte(rep.MD))
}

// renderPDFHTML executes the PDF template for rep, deriving HTML from MD (htmlOf) when
// the HTML column wasn't persisted — md-only reports don't store a redundant copy.
func (s *Server) renderPDFHTML(rep *Rep) (string, error) {
	data := *rep
	data.HTML = htmlOf(data)
	var buf strings.Builder
	if err := s.pdf.ExecuteTemplate(&buf, "pdf.html", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) reportPDF(w http.ResponseWriter, r *http.Request, user string) {
	rep := s.loadRep(r.PathValue("rid"))
	if rep == nil {
		http.Error(w, "报告不存在", 404)
		return
	}
	renderedHTML, err := s.renderPDFHTML(rep)
	if err != nil {
		http.Error(w, "render", 500)
		return
	}
	pdf, err := htmlToPDF(renderedHTML)
	if err == ErrNoWkhtmltopdf {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(503)
		fmt.Fprint(w, `<div style="font-family:sans-serif;max-width:520px;margin:12vh auto;text-align:center;color:#334">`+
			`<h2 style="color:#0c447c">PDF 暂不可用</h2>`+
			`<p>本机未安装 <code>wkhtmltopdf</code>，无法在本地生成 PDF。</p>`+
			`<p><b>Docker 部署已内置</b>，线上正常。想本地用可装：<br><code>brew install --cask wkhtmltopdf</code></p>`+
			`<p>也可先用 <b>⬇ MD</b> 导出。</p>`+
			`<p><a href="#" onclick="window.close();return false;">关闭此页</a> · <a href="/">返回首页</a></p></div>`)
		return
	}
	if err != nil {
		http.Error(w, "PDF 生成失败: "+err.Error(), 500)
		return
	}
	fn := safeFile(rep.Title, rid(r)) + ".pdf"
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	w.Write(pdf)
}

func rid(r *http.Request) string { return r.PathValue("rid") }

func safeFile(title, fallback string) string {
	if strings.TrimSpace(title) == "" {
		return fallback
	}
	return title
}

// ---------- Entry-button management ----------

// ---------- Report-type management ----------

var kindOrder = []string{"重组决策", "投资决策", "深度研究", "技术分析", "未分类"}

// ---------- Account management ----------

// ---------- System settings ----------
// Old-portal credentials are stored in the DB and set via System Settings; the
// one-shot importer (report-portal import-legacy) reads them. There is no live
// sync/read-through anymore — legacy data is migrated into the reports table.

func uniqSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
