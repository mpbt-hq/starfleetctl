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
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/dashboard"
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
// from the environment exactly like `agent-bus` (STARFLEET_SHIP_ID etc.).
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
	s.mux.HandleFunc("/api/tell", s.apiTell)
	s.mux.HandleFunc("/api/task", s.apiTask)
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
	s.mux.HandleFunc("/", s.serveIndex)
}

// Run starts the HTTP server (blocking).
func (s *Server) Run() error {
	fmt.Printf("starfleet web: listening on http://%s  (workspace: %s)\n", s.Addr, s.Root)
	return http.ListenAndServe(s.Addr, s.mux)
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
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "bad json: "+err.Error())
			return
		}
	} else {
		_ = r.ParseForm()
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
		code, err = task.RunCaptureOnly(s.Root, p.Title, p.Desc, assign, noPush)
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
		ID           string `json:"id"`            // unique key (auto-generated if empty)
		Description  string `json:"description"`   // human-readable description
		ScheduleType string `json:"schedule_type"` // "once"|"interval"|"cron"
		At           string `json:"at"`
		Every        string `json:"every"`
		Cron         string `json:"cron"`
		Type         string `json:"type"`        // "ship" (directive) or "command"
		Text         string `json:"text"`        // message body or command verb+args
		TargetType   string `json:"target_type"` // "ship"|"fleet"|"fleet-all"
		TargetValue  string `json:"target_value"`
		Persistent   *bool  `json:"persistent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if p.Text == "" {
		writeErr(w, 400, "text required")
		return
	}
	if p.ScheduleType == "" {
		writeErr(w, 400, "schedule_type required")
		return
	}
	if p.Type == "" {
		p.Type = "ship"
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
		writeErr(w, 404, "not found — use /api/ship/<id>/screen or /api/ship/<id>/stop")
		return
	}
	id, action := parts[0], parts[1]
	switch action {
	case "screen":
		s.apiShipScreen(w, r, id)
	case "stop":
		s.apiShipStop(w, r, id)
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
