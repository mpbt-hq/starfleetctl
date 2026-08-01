// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package web is a minimalist, mobile-first web interface for the fleet. It
// does NOT reimplement any fleet logic — it drives the EXISTING starfleetctl
// packages (comms, dashboard, task) in-process, exactly like the CLI
// subcommands do, so the web UI and `starfleetctl <cmd>` stay in lockstep.
// The frontend (embedded index.html) is plain HTML/CSS/JS with no dependencies,
// so it renders well even on tiny mobile screens and never needs a build step.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/filestore"
	"github.com/metux/starfleetctl/internal/ocsessions"
	"github.com/metux/starfleetctl/internal/reports"
	"github.com/metux/starfleetctl/internal/session"
	"github.com/metux/starfleetctl/internal/task"
	"github.com/metux/starfleetctl/internal/timer"
)

//go:embed index.html
var indexFS embed.FS

// Server holds the resolved workspace root + HTTP handler.
type Server struct {
	Root string
	Addr string
	bus  *comms.Bus
	dash *dashboard.Dashboard
	mux  *http.ServeMux
}

// New builds a web Server rooted at the given workspace root, bound to addr
// (e.g. ":8080" or "127.0.0.1:8080"). The comms board identity is taken
// from the environment exactly like `comms` (STARFLEET_SHIP_ID etc.).
func New(root, addr string) (*Server, error) {
	b, err := comms.New(root)
	if err != nil {
		return nil, fmt.Errorf("web: comms: %w", err)
	}
	// Allow the web frontend's bus identity to be configured via the web
	// config file (web.ship_id / web.ship_handle) or the STARFLEET_WEB_SHIP_ID
	// env override, independent of the process environment the server was
	// launched from. Without this, the frontend would appear on the bus under
	// whatever STARFLEET_SHIP_ID the launching shell happened to
	// export (or user@host), which is why it could show the wrong ship name.
	if cfg, cerr := config.Load(root); cerr == nil {
		shipID := os.Getenv("STARFLEET_WEB_SHIP_ID")
		if shipID == "" {
			shipID = cfg.Web.ShipID
		}
		if shipID != "" {
			b.ShipID = shipID
			b.ShipIDSet = true
			// Propagate so any spawn/child shares the same bus identity.
			_ = os.Setenv("STARFLEET_SHIP_ID", shipID)
			if h := cfg.Web.ShipHandle; h != "" {
				b.Handle = h
				_ = os.Setenv("STARFLEET_AGENT_HANDLE", h)
			}
		}
	}
	d, err := dashboard.New(root)
	if err != nil {
		return nil, fmt.Errorf("web: dashboard: %w", err)
	}
	s := &Server{Root: root, Addr: addr, bus: b, dash: d, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Handler returns the http.Handler for embedding / testing.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/api/board", s.apiBoard)
	s.mux.HandleFunc("/api/msgs", s.apiMsgs)
	s.mux.HandleFunc("/api/inbox", s.apiInbox)
	s.mux.HandleFunc("/api/asks", s.apiAsks)
	s.mux.HandleFunc("/api/events", s.apiEvents)
	s.mux.HandleFunc("/api/tasks", s.apiTasks)
	s.mux.HandleFunc("/api/dashboard/reindex", s.apiDashboardReindex)
	s.mux.HandleFunc("/api/tell", s.apiTell)
	s.mux.HandleFunc("/api/cmd", s.apiCmd)
	s.mux.HandleFunc("/api/task", s.apiTask)
	s.mux.HandleFunc("/api/topic/", s.apiTopicDispatch)
	s.mux.HandleFunc("/api/identity", s.apiIdentity)
	s.mux.HandleFunc("/api/models", s.apiModels)
	s.mux.HandleFunc("/api/timers", s.apiTimers)
	s.mux.HandleFunc("/api/timer", s.apiTimerCreate)
	s.mux.HandleFunc("/api/timer/", s.apiTimerDispatch)
	s.mux.HandleFunc("/api/timer/worker", s.apiTimerWorker)
	s.mux.HandleFunc("/api/web/restart", s.apiWebRestart)
	s.mux.HandleFunc("/api/ship", s.apiShipLaunch)
	s.mux.HandleFunc("/api/ship/", s.apiShipDispatch)
	s.mux.HandleFunc("/api/ships", s.apiShips)
	s.mux.HandleFunc("/api/store/", s.apiStoreFile)
	s.mux.HandleFunc("/api/files", s.apiFileList)
	s.mux.HandleFunc("/api/files/raw", s.apiFileRaw)
	s.mux.HandleFunc("/api/files/save", s.apiFileSave)
	s.mux.HandleFunc("/api/reports", s.apiReports)
	s.mux.HandleFunc("/api/reports/", s.apiReportDispatch)
	s.mux.HandleFunc("/api/sessions", s.apiSessions)
	s.mux.HandleFunc("/api/sessions/", s.apiSessionDispatch)
	s.mux.HandleFunc("/api/oclog", s.apiOCLog)
	s.mux.HandleFunc("/", s.serveIndex)
}

// Run starts the HTTP server (blocking). Registers the web frontend on the
// fleet board (heartbeat) and refreshes it periodically so the server appears
// as a live ship, exactly like any CLI or opencode ship.
func (s *Server) Run() error {
	cfg, cerr := config.Load(s.Root)
	if cerr != nil {
		cfg = config.DefaultConfig()
	}
	interval := time.Duration(cfg.Comms.HeartbeatMS) * time.Millisecond
	if interval <= 0 {
		interval = 300 * time.Second
	}

	// Post initial heartbeat to register on the fleet board.
	_ = s.bus.DoStatus("idle", "web console", comms.StatusPatch{
		LaunchType: "web",
	})

	// Periodic heartbeat refresh so the board sees a live ship.
	done := make(chan struct{})
	go s.heartbeatLoop(interval, done)

	// Clean shutdown on SIGINT/SIGTERM: clear heartbeat from board.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		close(done)
		_ = s.bus.DoClear()
		os.Exit(0)
	}()

	fmt.Printf("starfleet web: listening on http://%s  (workspace: %s)\n", s.Addr, s.Root)
	err := http.ListenAndServe(s.Addr, s.mux)
	close(done)
	_ = s.bus.DoClear()
	return err
}

// heartbeatLoop refreshes the heartbeat timestamp every interval until done
// is closed.  Matches the pattern used by comms-monitor-loop (DoTouch).
func (s *Server) heartbeatLoop(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = s.bus.DoTouch()
		case <-done:
			return
		}
	}
}

// ---- API helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func (s *Server) apiBoard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.bus.BoardEntries())
}

func (s *Server) apiMsgs(w http.ResponseWriter, r *http.Request) {
	// Optional ?ship=<name> filter: only messages involving that ship
	// (sent by it, addressed to it, or a broadcast to all). Used by the
	// per-ship conversation view in the frontend.
	if ship := strings.TrimSpace(r.URL.Query().Get("ship")); ship != "" {
		writeJSON(w, s.bus.ConversationWithViewer(ship, s.bus.ShipID))
		return
	}
	writeJSON(w, s.bus.AllMsgRecordsJSON())
}

func (s *Server) apiInbox(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.bus.AllInboxRecordsJSON())
}

func (s *Server) apiAsks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.bus.AllAskRecordsJSON())
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	n := 20
	if v := r.URL.Query().Get("n"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	writeJSON(w, s.bus.TailEvents(n))
}

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	metas, err := s.dash.LoadAllTopicsJSON()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, metas)
}

// apiTopicDispatch routes /api/topic/<slug> — full-text view/edit of a
// dashboard topic (used for the task detail view):
//
//	GET  -> full topic (frontmatter + markdown body)
//	POST {title?, body, commit_msg?, push?} -> rewrite body (+ title) via the
//	      sanctioned dashboard path and commit (+ push by default)
func (s *Server) apiTopicDispatch(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/topic/")
	if slug == "" {
		writeErr(w, 400, "slug required — /api/topic/<slug>")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.apiTopicGet(w, slug)
	case http.MethodPost:
		s.apiTopicUpdate(w, r, slug)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) apiTopicGet(w http.ResponseWriter, slug string) {
	m, body, err := s.dash.DoTopicLoad(slug)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"slug":        m.Slug,
		"title":       m.Title,
		"body":        body,
		"kind":        m.Kind,
		"status":      m.Status,
		"assigned_to": m.AssignedTo,
		"created_by":  m.CreatedBy,
		"created":     m.Created,
	})
}

// apiTopicUpdate rewrites a topic's full text (and optionally its title)
// through the sanctioned dashboard path (DoTopicLoad -> DoTopicUpdate ->
// DoTopicCommit), never touching the topic file directly. It commits and,
// unless push=false, pushes — an explicit full-text edit is meant to
// propagate to the fleet; if the git remote is unreachable the error
// surfaces to the caller instead of silently dropping the push.
func (s *Server) apiTopicUpdate(w http.ResponseWriter, r *http.Request, slug string) {
	var p struct {
		Title     string `json:"title"`
		Body      string `json:"body"`
		CommitMsg string `json:"commit_msg"`
		Push      *bool  `json:"push"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	m, _, err := s.dash.DoTopicLoad(slug)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if p.Title != "" {
		m.Title = p.Title
	}
	msg := strings.TrimSpace(p.CommitMsg)
	if msg == "" {
		msg = "web: topic update " + slug
	}
	push := true
	if p.Push != nil {
		push = *p.Push
	}
	if err := s.dash.DoTopicUpdate(slug, m, p.Body); err != nil {
		writeErr(w, 500, "write: "+err.Error())
		return
	}
	if err := s.dash.DoTopicCommit(slug, msg, push); err != nil {
		writeErr(w, 500, "commit: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "slug": slug})
}

// apiDashboardReindex handles POST /api/dashboard/reindex — regenerates the
// thin index tables in DASHBOARD.md from every dashboard/topics/*.md file's
// frontmatter. Needed after topics are added/edited directly in the
// filesystem: the task list itself reads the topic files live, but the
// index (which `dashboard topic list` / task dedup read) only updates when a
// reindex runs.
func (s *Server) apiDashboardReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if err := s.dash.DoReindex(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiIdentity reports the web server's own fleet identity (what the bus sees
// the viewer as) so the UI can show "you are: <ship>" and default the
// sender of tells appropriately.
func (s *Server) apiIdentity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"ship_id": s.bus.ShipID,
		"handle":  s.bus.Handle,
		"project": s.bus.Project,
	})
}

// apiTell POSTs a directive: {"target": "all"|"<ship>", "text": "..."}.
// Delegates to comms.Tell / broadcast — the same code path as
// `comms tell` / `comms broadcast`. Body via JSON or form.
func (s *Server) apiTell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	target, text, replyTo := "", "", ""
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var p struct {
			Target  string `json:"target"`
			Text    string `json:"text"`
			ReplyTo string `json:"reply_to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json: "+err.Error())
			return
		}
		target, text = p.Target, p.Text
		replyTo = strings.TrimSpace(p.ReplyTo)
	} else {
		target = r.FormValue("target")
		text = r.FormValue("text")
		replyTo = strings.TrimSpace(r.FormValue("reply_to"))
	}
	target = strings.TrimSpace(target)
	text = strings.TrimSpace(text)
	if target == "" || text == "" {
		writeErr(w, 400, "target and text are required")
		return
	}
	id, err := s.bus.Tell(target, text, replyTo)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "target": target, "reply_to": replyTo})
}

// apiCmd posts a command (type="command") to a ship — same as
// `comms cmd <target> <verb> [args]`. Unlike apiTell, the payload
// is dispatched through the plugin's command handler, not injected
// as a system prompt.
func (s *Server) apiCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var p struct {
		Target string `json:"target"`
		Verb   string `json:"verb"`
		Args   string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p.Target = strings.TrimSpace(p.Target)
	p.Verb = strings.TrimSpace(p.Verb)
	if p.Target == "" || p.Verb == "" {
		writeErr(w, 400, "target and verb are required")
		return
	}
	id, err := s.bus.Command(p.Target, p.Verb, p.Args)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "target": p.Target, "verb": p.Verb})
}

// apiTask captures or mutates a dashboard task via the sanctioned task
// package — never touches topic files directly. Accepts:
//
//	POST   {title, desc?, assign?, status?}  -> task capture
//	POST   {slug, ship?}                     -> task assign
//	POST   {slug}                            -> task unassign
//	POST   {slug, status}                    -> task status
func (s *Server) apiTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var p struct {
		Title    string `json:"title"`
		Desc     string `json:"desc"`
		Slug     string `json:"slug"`
		Ship     string `json:"ship"`
		Assign   string `json:"assign"` // "" | "auto"/"__auto__" | "<ship>"
		Status   string `json:"status"`
		Unassign bool   `json:"unassign"`
		Category string `json:"category"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json: "+err.Error())
			return
		}
	} else {
		// ParseForm() alone makes r.Form non-nil before FormValue() runs, which
		// suppresses FormValue's implicit multipart parse — so a FormData body
		// (what the web UI's fetch() sends) would yield empty fields. Parse
		// both explicitly.
		_ = r.ParseForm()
		_ = r.ParseMultipartForm(32 << 20)
		p.Title = r.FormValue("title")
		p.Desc = r.FormValue("desc")
		p.Slug = r.FormValue("slug")
		p.Ship = r.FormValue("ship")
		p.Assign = r.FormValue("assign")
		p.Status = r.FormValue("status")
		p.Unassign = r.FormValue("unassign") == "1" || r.FormValue("unassign") == "true"
	}

	// Normalize the assign token: "auto" / "" from the UI => "__auto__"
	// sentinel understood by the task package.
	assign := strings.TrimSpace(p.Assign)
	if assign == "auto" {
		assign = "__auto__"
	}

	// Tasks are captured locally (noPush) by default: a LAN viewer must never
	// block on a (possibly unreachable) git remote. The dashboard reindex +
	// bus directive still happen; only the push to origin is skipped.
	const noPush = true

	var code int
	var err error
	switch {
	case p.Slug != "" && p.Status != "":
		code, err = task.RunCaptureStatus(s.Root, p.Slug, p.Status, noPush)
	case p.Slug != "" && p.Unassign:
		code, err = task.RunUnassignOnly(s.Root, p.Slug, noPush)
	case p.Slug != "" && (p.Ship != "" || assign != ""):
		ship := p.Ship
		if ship == "" {
			ship = assign
		}
		code, err = task.RunAssignOnly(s.Root, p.Slug, ship, noPush)
	case p.Title != "":
		code, err = task.RunCaptureOnly(s.Root, p.Title, p.Desc, assign, p.Category, noPush)
	default:
		writeErr(w, 400, "need title (capture) or slug+status / slug+assign / slug(unassign)")
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if code != 0 {
		writeErr(w, 422, fmt.Sprintf("task command exited with code %d", code))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiTimers lists all timers across all stores.
func (s *Server) apiTimers(w http.ResponseWriter, r *http.Request) {
	showAll := r.URL.Query().Get("all") == "1"
	var all []*timer.TimerRecord
	for _, td := range timer.TimerDirs(s.Root) {
		store, err := timer.NewStore(td.Dir)
		if err != nil {
			continue
		}
		timers, err := store.List()
		if err != nil {
			continue
		}
		all = append(all, timers...)
	}
	if !showAll {
		var filtered []*timer.TimerRecord
		for _, t := range all {
			if t.Owner == s.bus.ShipID {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	if all == nil {
		all = []*timer.TimerRecord{}
	}
	writeJSON(w, all)
}

// apiTimerCreate handles POST /api/timer for creating timers.
func (s *Server) apiTimerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	s.timerCreate(w, r)
}

// apiTimerDispatch handles /api/timer/{id}, /api/timer/{id}/pause, /api/timer/{id}/resume.
func (s *Server) apiTimerDispatch(w http.ResponseWriter, r *http.Request) {
	// Strip /api/timer/ prefix to get the path remainder.
	rest := strings.TrimPrefix(r.URL.Path, "/api/timer/")

	if rest == "" || rest == "worker" {
		writeErr(w, 400, "need timer id")
		return
	}

	// Check for /{id}/pause or /{id}/resume.
	if strings.HasSuffix(rest, "/pause") || strings.HasSuffix(rest, "/resume") {
		s.timerToggle(w, r, rest)
		return
	}

	// Plain DELETE /api/timer/{id}.
	if r.Method == http.MethodDelete {
		s.timerDelete(w, r, rest)
		return
	}
	writeErr(w, 405, "method not allowed")
}

func (s *Server) timerCreate(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ID           string   `json:"id"`            // unique key (auto-generated if empty)
		Description  string   `json:"description"`   // human-readable description
		ScheduleType string   `json:"schedule_type"` // "once"|"interval"|"cron"
		At           string   `json:"at"`
		Every        string   `json:"every"`
		Cron         string   `json:"cron"`
		Type         string   `json:"type"`        // "ship" (directive), "command", or "system"
		Text         string   `json:"text"`        // message body or command verb+args
		Cmd          []string `json:"cmd"`         // command line (system only)
		TargetType   string   `json:"target_type"` // "ship"|"fleet"|"fleet-all"|"system"
		TargetValue  string   `json:"target_value"`
		Persistent   *bool    `json:"persistent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if p.ScheduleType == "" {
		writeErr(w, 400, "schedule_type required")
		return
	}

	// System timers use cmd[] instead of text.
	isSystem := p.TargetType == "system" || p.Type == "system"
	if isSystem {
		if len(p.Cmd) == 0 {
			writeErr(w, 400, "cmd required for system timers")
			return
		}
		p.Type = "system"
	} else {
		if p.Text == "" {
			writeErr(w, 400, "text required")
			return
		}
		if p.Type == "" {
			p.Type = "ship"
		}
	}

	// Auto-generate ID if not given.
	if p.ID == "" {
		p.ID = timer.GenerateName()
	}

	// Parse target type.
	tt := timer.TargetShip
	switch p.TargetType {
	case "fleet":
		tt = timer.TargetFleet
	case "fleet-all":
		tt = timer.TargetFleetAll
	case "system":
		tt = timer.TargetSystem
	}
	tgtVal := p.TargetValue
	if tt == timer.TargetShip && tgtVal == "" {
		tgtVal = s.bus.ShipID
	}

	// Parse schedule.
	var sched timer.Schedule
	var nextFire int64
	switch timer.ScheduleType(p.ScheduleType) {
	case timer.ScheduleOnce:
		sched = timer.Schedule{Type: timer.ScheduleOnce}
		t, err := timer.ParseAtTime(p.At, "")
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		nextFire = t.Unix()
	case timer.ScheduleInterval:
		d, err := time.ParseDuration(p.Every)
		if err != nil {
			writeErr(w, 400, "invalid every: "+err.Error())
			return
		}
		sched = timer.Schedule{Type: timer.ScheduleInterval, IntervalSec: int64(d.Seconds())}
		nextFire = time.Now().UTC().Add(d).Unix()
	case timer.ScheduleCron:
		sched = timer.Schedule{Type: timer.ScheduleCron, CronExpr: p.Cron}
		next, err := timer.CronNextFire(p.Cron, "")
		if err != nil {
			writeErr(w, 400, "invalid cron: "+err.Error())
			return
		}
		nextFire = next.Unix()
	default:
		writeErr(w, 400, "unknown schedule_type: "+p.ScheduleType)
		return
	}

	// Persistence: default cron → persistent, others → ephemeral.
	persistent := timer.ScheduleType(p.ScheduleType) == timer.ScheduleCron
	if p.Persistent != nil {
		persistent = *p.Persistent
	}

	rec := &timer.TimerRecord{
		ID:          p.ID,
		Description: p.Description,
		Owner:       s.bus.ShipID,
		Target:      timer.TargetSpec{Type: tt, Value: tgtVal},
		Type:        p.Type,
		Text:        p.Text,
		Cmd:         p.Cmd,
		Schedule:    sched,
		Persistent:  persistent,
		Enabled:     true,
		CreatedAt:   time.Now().Unix(),
		NextFire:    nextFire,
	}

	store, err := timer.PickStore(s.Root, persistent)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	id, err := store.Create(rec)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	timer.NotifyWorker(s.Root)
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) timerDelete(w http.ResponseWriter, r *http.Request, id string) {
	for _, td := range timer.TimerDirs(s.Root) {
		store, err := timer.NewStore(td.Dir)
		if err != nil {
			continue
		}
		if _, err := store.Get(id); err == nil {
			if err := store.Delete(id); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			timer.NotifyWorker(s.Root)
			writeJSON(w, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, 404, "timer not found")
}

func (s *Server) timerToggle(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	disable := strings.HasSuffix(path, "/pause")
	id := strings.TrimSuffix(strings.TrimSuffix(path, "/pause"), "/resume")

	for _, td := range timer.TimerDirs(s.Root) {
		store, err := timer.NewStore(td.Dir)
		if err != nil {
			continue
		}
		rec, err := store.Get(id)
		if err == nil {
			rec.Enabled = !disable
			if err := store.Update(rec); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			timer.NotifyWorker(s.Root)
			writeJSON(w, map[string]any{"ok": true})
			return
		}
	}
	writeErr(w, 404, "timer not found")
}

// apiWebRestart restarts the web server daemon (stop + start).
func (s *Server) apiWebRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if err := webRestart(s.Root); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// webRestart stops and restarts the web server. The new process takes over
// the port after the current process is killed.
func webRestart(root string) error {
	return Restart(root)
}

// apiShipLaunch POSTs a new ship (the web console's "new ship" action).
// Body: {"name":"", "model":"provider/model", "parent":""}.
//
//	name   — optional; empty => next free ship name
//	model  — optional opencode model id (provider derived from it)
//	parent — optional ship to hang under; empty => flagship (Enterprise),
//	         since a web-GUI launch is treated as an auto-launch under the
//	         flagship. The launch_type is always "auto" for web launches.
//
// Delegates to session.LaunchShip — the same code path as `session ship-run`,
// so the detached termctl terminal, registry, and heartbeat are identical.
func (s *Server) apiShipLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var p struct {
		Name     string `json:"name"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Parent   string `json:"parent"`
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
	} else {
		p.Name = r.FormValue("name")
		p.Model = r.FormValue("model")
		p.Provider = r.FormValue("provider")
		p.Parent = r.FormValue("parent")
	}
	shipID, err := session.LaunchShip(s.Root, session.LaunchShipOpts{
		Name:       p.Name,
		Model:      p.Model,
		Provider:   p.Provider,
		Parent:     p.Parent,
		LaunchType: "auto",
	})
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "ship_id": shipID})
}

// apiShipDispatch routes /api/ship/<id>/... to the appropriate handler.
func (s *Server) apiShipDispatch(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/ship/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 {
		writeErr(w, 404, "not found — use /api/ship/<id>/screen or /api/ship/<id>/stop or /api/ship/<id>/dimensions")
		return
	}
	id, action := parts[0], parts[1]
	switch action {
	case "screen":
		s.apiShipScreen(w, r, id)
	case "stop":
		s.apiShipStop(w, r, id)
	case "dimensions":
		s.apiShipDimensions(w, r)
	default:
		writeErr(w, 404, "unknown action: "+action)
	}
}

// apiShipStop stops a running ship.
func (s *Server) apiShipStop(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeErr(w, 405, "method not allowed")
		return
	}
	if err := session.StopShip(s.Root, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "ship_id": id})
}

// apiShips returns all known ships with their status, model, and health info.
func (s *Server) apiShips(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	records := s.bus.AllStatusRecords()
	type shipInfo struct {
		Name     string `json:"name"`
		State    string `json:"state"`
		PID      int    `json:"pid,omitempty"`
		Handle   string `json:"handle,omitempty"`
		Note     string `json:"note,omitempty"`
		Model    string `json:"model,omitempty"`
		Server   string `json:"server,omitempty"`
		Task     string `json:"task,omitempty"`
		Progress int    `json:"progress,omitempty"`
		Blocker  string `json:"blocker,omitempty"`
	}
	var ships []shipInfo
	for _, rec := range records {
		info := shipInfo{
			Name:     rec.Agent,
			State:    rec.State,
			PID:      rec.PID,
			Handle:   rec.Handle,
			Note:     rec.Note,
			Model:    rec.Model,
			Server:   rec.Server,
			Task:     rec.Task,
			Progress: rec.Progress,
			Blocker:  rec.Blocker,
		}
		ships = append(ships, info)
	}
	writeJSON(w, ships)
}

// apiShipScreen returns the current terminal screen content for a ship.
func (s *Server) apiShipScreen(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}

	// Resolve the ship's termctl pipe
	pipePath, ok := session.ResolvePipe(s.Root, id)
	if !ok {
		writeErr(w, 404, "no running terminal for "+id)
		return
	}

	// Check if scrollback is requested
	scrollbackStr := r.URL.Query().Get("scrollback")
	if scrollbackStr != "" {
		n := 100
		fmt.Sscanf(scrollbackStr, "%d", &n)
		lines, err := session.ScreenDumpScrollback(pipePath, n)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ship_id": id, "lines": lines, "type": "scrollback"})
		return
	}

	// Default: dump visible screen
	lines, err := session.ScreenDump(pipePath)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ship_id": id, "lines": lines, "type": "screen"})
}

// apiShipDimensions returns the terminal dimensions for a ship.
func (s *Server) apiShipDimensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/ship/")
	id = strings.TrimSuffix(id, "/dimensions")
	if id == "" {
		writeErr(w, 400, "need ship id")
		return
	}

	pipePath, ok := session.ResolvePipe(s.Root, id)
	if !ok {
		writeErr(w, 404, "no running terminal for "+id)
		return
	}

	rows, cols, err := session.ScreenDimensions(pipePath)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ship_id": id, "rows": rows, "cols": cols})
}

// apiStoreFile serves files from the agent file store.
// GET /api/store/<name>  — serves the file with infered Content-Type.
func (s *Server) apiStoreFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/store/")
	if name == "" || strings.Contains(name, "/") {
		writeErr(w, 400, "invalid file name")
		return
	}
	store, err := filestore.New(s.Root)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !store.Exists(name) {
			writeErr(w, 404, "file not found or expired")
			return
		}
		path := store.Path(name)
		mime.AddExtensionType(filepath.Ext(name), "")
		ctype := mime.TypeByExtension(filepath.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		http.ServeFile(w, r, path)

	case http.MethodPost, http.MethodPut:
		r.ParseMultipartForm(32 << 20) // 32 MB max
		file, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, 400, "need file field: "+err.Error())
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			writeErr(w, 500, "read upload: "+err.Error())
			return
		}
		ttl := time.Hour
		if ttlStr := r.FormValue("ttl"); ttlStr != "" {
			if d, err := time.ParseDuration(ttlStr); err == nil {
				ttl = d
			}
		}
		// Write via temp file for filestore.Put
		tmp := filepath.Join(os.TempDir(), "stf-upload-"+name)
		if err := os.WriteFile(tmp, data, 0644); err != nil {
			writeErr(w, 500, "write temp: "+err.Error())
			return
		}
		defer os.Remove(tmp)
		url, err := store.Put(tmp, ttl)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "name": name, "url": url})

	default:
		writeErr(w, 405, "method not allowed")
	}
}

// apiFileList returns the contents of a directory within the workspace.
// GET /api/files?path=<relpath>  — relpath defaults to "." (workspace root).
// Returns JSON array of {name, is_dir, size, mod_time}.
func (s *Server) apiFileList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		rel = "."
	}
	abs := filepath.Join(s.Root, filepath.Clean(rel))
	// Ensure the resolved path stays inside the workspace root.
	if !strings.HasPrefix(abs, filepath.Clean(s.Root)+string(filepath.Separator)) && abs != filepath.Clean(s.Root) {
		writeErr(w, 403, "path escapes workspace root")
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	type fileEntry struct {
		Name    string `json:"name"`
		IsDir   bool   `json:"is_dir"`
		Size    int64  `json:"size,omitempty"`
		ModTime int64  `json:"mod_time"`
	}
	var files []fileEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
	}
	if files == nil {
		files = []fileEntry{}
	}
	writeJSON(w, map[string]any{"path": rel, "entries": files})
}

// apiFileRaw serves a single file's content for viewing or downloading.
// GET /api/files/raw?path=<relpath>&download=1
// For text files the content is served inline; for binary files or when
// download=1 is set, Content-Disposition: attachment is used.
func (s *Server) apiFileRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		writeErr(w, 400, "path required")
		return
	}
	abs := filepath.Join(s.Root, filepath.Clean(rel))
	if !strings.HasPrefix(abs, filepath.Clean(s.Root)+string(filepath.Separator)) {
		writeErr(w, 403, "path escapes workspace root")
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, 400, "path is a directory — use /api/files to list")
		return
	}
	// Limit to 2 MB for inline viewing.
	const maxSize = 2 * 1024 * 1024
	if info.Size() > maxSize && r.URL.Query().Get("download") != "1" {
		writeErr(w, 413, fmt.Sprintf("file too large (%d bytes) — use ?download=1", info.Size()))
		return
	}
	ext := filepath.Ext(abs)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(abs))
	}
	http.ServeFile(w, r, abs)
}

// apiFileSave saves a file with YAML comment preservation.
// POST /api/files/save with JSON body: {"path": "<relpath>", "content": "<new content>"}
// Uses gopkg.in/yaml.v3 to round-trip YAML files while preserving comments.
func (s *Server) apiFileSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p.Path = strings.TrimSpace(p.Path)
	if p.Path == "" {
		writeErr(w, 400, "path required")
		return
	}
	abs := filepath.Join(s.Root, filepath.Clean(p.Path))
	if !strings.HasPrefix(abs, filepath.Clean(s.Root)+string(filepath.Separator)) && abs != filepath.Clean(s.Root) {
		writeErr(w, 403, "path escapes workspace root")
		return
	}
	// Check if file exists
	if _, err := os.Stat(abs); err != nil {
		writeErr(w, 404, "file not found")
		return
	}
	// For YAML files, use round-trip to preserve comments
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".yaml" || ext == ".yml" {
		if err := saveYAMLWithComments(abs, p.Content); err != nil {
			writeErr(w, 500, "failed to save YAML: "+err.Error())
			return
		}
	} else {
		// For non-YAML files, just write the content
		if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
			writeErr(w, 500, "failed to save file: "+err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "path": p.Path})
}

// saveYAMLWithComments preserves comments when saving YAML files.
// It loads the original YAML with comments, updates the values from newContent,
// and writes back preserving the comment structure.
func saveYAMLWithComments(path, newContent string) error {
	// Read original file to preserve comments
	origData, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Parse new content to get the updated values
	var newNode yaml.Node
	if err := yaml.Unmarshal([]byte(newContent), &newNode); err != nil {
		return fmt.Errorf("invalid YAML in new content: %w", err)
	}
	// Parse original with comments
	var origNode yaml.Node
	if err := yaml.Unmarshal(origData, &origNode); err != nil {
		// If original has parse errors, fall back to writing new content directly
		return os.WriteFile(path, []byte(newContent), 0o644)
	}
	// Merge: replace original document content with new content but keep original comments
	// by copying the new content into the original document structure
	mergedNode := mergeYAMLNodes(&origNode, &newNode)
	// Marshal back to YAML
	out, err := yaml.Marshal(mergedNode)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// mergeYAMLNodes recursively merges newNode into origNode, preserving origNode's comments.
// It replaces the content but keeps comments attached to nodes.
func mergeYAMLNodes(origNode, newNode *yaml.Node) *yaml.Node {
	if origNode == nil {
		return newNode
	}
	if newNode == nil {
		return origNode
	}
	// For document nodes, merge the first (and usually only) content
	if origNode.Kind == yaml.DocumentNode && newNode.Kind == yaml.DocumentNode {
		if len(origNode.Content) > 0 && len(newNode.Content) > 0 {
			merged := mergeYAMLNodes(origNode.Content[0], newNode.Content[0])
			return &yaml.Node{
				Kind:        yaml.DocumentNode,
				Content:     []*yaml.Node{merged},
				HeadComment: origNode.HeadComment,
				LineComment: origNode.LineComment,
				FootComment: origNode.FootComment,
			}
		}
	}
	// For mapping nodes (objects), merge keys
	if origNode.Kind == yaml.MappingNode && newNode.Kind == yaml.MappingNode {
		// Build a map of new values for quick lookup
		newMap := make(map[string]*yaml.Node)
		for i := 0; i < len(newNode.Content); i += 2 {
			if i+1 < len(newNode.Content) {
				key := newNode.Content[i].Value
				newMap[key] = newNode.Content[i+1]
			}
		}
		// Build merged content preserving original order and comments
		var mergedContent []*yaml.Node
		for i := 0; i < len(origNode.Content); i += 2 {
			if i+1 >= len(origNode.Content) {
				continue
			}
			keyNode := origNode.Content[i]
			valNode := origNode.Content[i+1]
			key := keyNode.Value
			if newVal, ok := newMap[key]; ok {
				// Key exists in new - merge recursively
				mergedVal := mergeYAMLNodes(valNode, newVal)
				mergedContent = append(mergedContent, keyNode, mergedVal)
				delete(newMap, key)
			} else {
				// Key only in original - keep it
				mergedContent = append(mergedContent, keyNode, valNode)
			}
		}
		// Add new keys that weren't in original
		for _, newVal := range newMap {
			// Need to find the key node for this value
			for i := 0; i < len(newNode.Content); i += 2 {
				if i+1 < len(newNode.Content) && newNode.Content[i+1] == newVal {
					mergedContent = append(mergedContent, newNode.Content[i], newVal)
					break
				}
			}
		}
		return &yaml.Node{
			Kind:        yaml.MappingNode,
			Content:     mergedContent,
			HeadComment: origNode.HeadComment,
			LineComment: origNode.LineComment,
			FootComment: origNode.FootComment,
		}
	}
	// For sequence nodes (arrays), replace entirely (complex to merge)
	if origNode.Kind == yaml.SequenceNode && newNode.Kind == yaml.SequenceNode {
		// Keep original's comments, use new content
		newContent := make([]*yaml.Node, len(newNode.Content))
		copy(newContent, newNode.Content)
		return &yaml.Node{
			Kind:        yaml.SequenceNode,
			Content:     newContent,
			HeadComment: origNode.HeadComment,
			LineComment: origNode.LineComment,
			FootComment: origNode.FootComment,
		}
	}
	// For scalar nodes and others, use new node but preserve original comments
	return &yaml.Node{
		Kind:        newNode.Kind,
		Value:       newNode.Value,
		Tag:         newNode.Tag,
		HeadComment: origNode.HeadComment,
		LineComment: origNode.LineComment,
		FootComment: origNode.FootComment,
	}
}

// apiModels returns the list of available models from models.yaml.
func (s *Server) apiModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	modelsPath := s.Root + "/.starfleet-ai/conf/models.yaml"
	data, err := os.ReadFile(modelsPath)
	if err != nil {
		writeErr(w, 404, "models.yaml not found — run gen-models-yaml")
		return
	}
	// Parse YAML manually (minimal: extract id, provider, label, context)
	type ModelEntry struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Context  int    `json:"context"`
	}
	var models []ModelEntry
	lines := strings.Split(string(data), "\n")
	var cur ModelEntry
	inModels := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "models:" {
			inModels = true
			continue
		}
		if !inModels {
			continue
		}
		if strings.HasPrefix(trimmed, "- id:") {
			if cur.ID != "" {
				models = append(models, cur)
			}
			cur = ModelEntry{ID: strings.Trim(strings.TrimPrefix(trimmed, "- id:"), " \"")}
		} else if strings.HasPrefix(trimmed, "provider:") {
			cur.Provider = strings.Trim(strings.TrimPrefix(trimmed, "provider:"), " \"")
		} else if strings.HasPrefix(trimmed, "label:") {
			cur.Label = strings.Trim(strings.TrimPrefix(trimmed, "label:"), " \"")
		} else if strings.HasPrefix(trimmed, "context:") {
			fmt.Sscanf(strings.TrimPrefix(trimmed, "context:"), "%d", &cur.Context)
		}
	}
	if cur.ID != "" {
		models = append(models, cur)
	}
	writeJSON(w, models)
}

// apiTimerWorker handles worker start/stop/status.
func (s *Server) apiTimerWorker(w http.ResponseWriter, r *http.Request) {
	running, pid := timer.WorkerStatus(s.Root)

	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"running": running, "pid": pid})
		return
	}

	if r.Method == http.MethodPost {
		var p struct {
			Action string `json:"action"` // "start"|"stop"|"restart"
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		switch p.Action {
		case "start":
			if running {
				writeJSON(w, map[string]any{"ok": true, "already_running": true, "pid": pid})
				return
			}
			if err := timer.StartWorker(s.Root); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		case "stop":
			if !running {
				writeJSON(w, map[string]any{"ok": true, "not_running": true})
				return
			}
			if err := timer.StopWorker(s.Root); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		case "restart":
			if err := timer.RestartWorker(s.Root); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			writeErr(w, 400, "action must be start, stop, or restart")
		}
		return
	}
	writeErr(w, 405, "method not allowed")
}

// apiReports handles GET /api/reports (list, with optional ?ship= and ?tag=)
// and POST /api/reports (submit).
func (s *Server) apiReports(w http.ResponseWriter, r *http.Request) {
	store, err := reports.NewStore(s.Root)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		filterShip := r.URL.Query().Get("ship")
		filterTag := r.URL.Query().Get("tag")
		all, err := store.ListJSON()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		var out []reports.ReportJSON
		for _, rec := range all {
			if filterShip != "" && rec.Ship != filterShip {
				continue
			}
			if filterTag != "" {
				hasTag := false
				for _, t := range rec.Tags {
					if t == filterTag {
						hasTag = true
						break
					}
				}
				if !hasTag {
					continue
				}
			}
			out = append(out, rec)
		}
		if out == nil {
			out = []reports.ReportJSON{}
		}
		writeJSON(w, out)

	case http.MethodPost:
		var p struct {
			Title       string   `json:"title"`
			Subtitle    string   `json:"subtitle"`
			Body        string   `json:"body"`
			Tags        []string `json:"tags"`
			TaskRef     string   `json:"task_ref"`
			Attachments []string `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json: "+err.Error())
			return
		}
		if p.Title == "" {
			writeErr(w, 400, "title required")
			return
		}
		rec := &reports.ReportRecord{
			ID:          fmt.Sprintf("r-%d", time.Now().UnixNano()),
			Title:       p.Title,
			Subtitle:    p.Subtitle,
			Ship:        s.bus.ShipID,
			Body:        p.Body,
			Tags:        p.Tags,
			TaskRef:     p.TaskRef,
			Attachments: p.Attachments,
		}
		id, err := store.Create(rec)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": id})

	default:
		writeErr(w, 405, "method not allowed")
	}
}

// apiReportDispatch handles GET /api/reports/<id> (show) and DELETE /api/reports/<id>.
func (s *Server) apiReportDispatch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/reports/")
	if id == "" {
		writeErr(w, 400, "need report id")
		return
	}
	store, err := reports.NewStore(s.Root)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rec, err := store.Get(id)
		if err != nil {
			writeErr(w, 404, "report not found")
			return
		}
		writeJSON(w, rec)
	case http.MethodDelete:
		if err := store.Delete(id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

// apiSessions handles GET /api/sessions — list opencode sessions, most
// recently updated first. Optional filters: ?title=<ship name> and
// ?agent=<mode> (build / plan / explore / general), plus ?limit=.
func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, 400, "limit must be a number")
			return
		}
		limit = n
	}
	sessions, err := ocsessions.List(ocsessions.ListOpts{
		Title: q.Get("title"),
		Mode:  q.Get("agent"),
		Limit: limit,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.markRunning(sessions)
	writeJSON(w, map[string]any{"sessions": sessions})
}

// apiSessionDispatch handles GET /api/sessions/<id> — the session's meta plus
// a chronological transcript window (?limit=, ?offset=).
func (s *Server) apiSessionDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if id == "" {
		writeErr(w, 400, "need session id")
		return
	}
	q := r.URL.Query()
	limit, offset := 0, 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, 400, "limit must be a number")
			return
		}
		limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, 400, "offset must be a number")
			return
		}
		offset = n
	}
	tr, err := ocsessions.SessionTranscript(id, limit, offset)
	if err != nil {
		code := 500
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "invalid") {
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	tr.Session.Running = s.liveShip(tr.Session.Title)
	writeJSON(w, tr)
}

// apiOCLog handles GET /api/oclog?n= — the last n lines (default 200) of the
// opencode client log.
func (s *Server) apiOCLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	n := 0
	if v := r.URL.Query().Get("n"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, 400, "n must be a number")
			return
		}
		n = parsed
	}
	if n < 0 || n > 5000 {
		writeErr(w, 400, "n must be between 0 and 5000")
		return
	}
	lines, err := ocsessions.TailLog(n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"lines": lines})
}

// liveShip reports whether a live (non-stale) board entry uses the given
// agent name — used to flag opencode sessions whose ship is currently up.
func (s *Server) liveShip(agent string) bool {
	if agent == "" {
		return false
	}
	for _, e := range s.bus.BoardEntries() {
		if !e.Stale && e.Agent == agent {
			return true
		}
	}
	return false
}

// markRunning flags each session whose title matches a live board entry.
func (s *Server) markRunning(list []ocsessions.Session) {
	for i := range list {
		list[i].Running = s.liveShip(list[i].Title)
	}
}

// serveIndex serves the embedded single-page frontend.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := indexFS.ReadFile("index.html")
	if err != nil {
		writeErr(w, 500, "index.html missing: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, strings.NewReader(string(data)))
}

// usage is the `web` subcommand help text.
const usage = `web [start|stop|autostart|restart] [options]

  Minimalist mobile-first fleet web console. Reuses the same in-process
  comms / dashboard / task code as the CLI subcommands, so the web UI
  and 'starfleetctl <cmd>' stay in lockstep. Defaults to listening on
  0.0.0.0:8080 (all interfaces, so it is reachable from other devices —
  e.g. a phone on the LAN).

  Subcommands:
    (none)           Show this help
    start            Start the web server in the foreground
    stop             Stop the web server daemon
    autostart        Start as daemon if not already running (cron-friendly)
    restart          Stop if running, then start as daemon

  Options (for start):
    --addr HOST:PORT Listen address (default: 0.0.0.0:8080)
    --no-browser     Accepted for CLI parity (no effect)

  Examples:
    starfleetctl web start              # foreground, http://:8080
    starfleetctl web start --addr :9090
    starfleetctl web autostart          # daemon, skip if running
    starfleetctl web stop               # kill daemon
    starfleetctl web restart            # stop + autostart
`

// Run dispatches a `web` invocation given the resolved workspace root.
// Returns the process exit code.
func Run(root string, args []string) int {
	// No args → show help
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}

	// Subcommand dispatch
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0

	case "start":
		return runStart(root, args[1:])

	case "stop":
		if err := Stop(root); err != nil {
			fmt.Fprintln(os.Stderr, "web stop:", err)
			return 1
		}
		fmt.Println("web server stopped")
		return 0

	case "autostart":
		ok, err := Autostart(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "web autostart:", err)
			return 1
		}
		if ok {
			fmt.Println("web server running")
		} else {
			fmt.Println("web server start failed")
		}
		return 0

	case "restart":
		if err := Restart(root); err != nil {
			fmt.Fprintln(os.Stderr, "web restart:", err)
			return 1
		}
		fmt.Println("web server restarted")
		return 0

	default:
		fmt.Fprintf(os.Stderr, "web: unknown subcommand: %s\n\n%s", args[0], usage)
		return 2
	}
}

// runStart handles `web start [--addr …] [--no-browser]`.
func runStart(root string, args []string) int {
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web start:", err)
		return 1
	}
	addr := cfg.Web.ListenAddr

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "web start: --addr needs a value")
				return 2
			}
			i++
			addr = args[i]
		case "--no-browser":
			// accepted for CLI parity; the server has no browser control
		default:
			fmt.Fprintf(os.Stderr, "web start: unknown option: %s\n\n%s", args[i], usage)
			return 2
		}
	}

	s, err := New(root, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web start:", err)
		return 1
	}
	if err := s.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "web start:", err)
		return 1
	}
	return 0
}
