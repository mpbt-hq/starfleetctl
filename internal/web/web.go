// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package web is a minimalist, mobile-first web interface for the fleet. It
// does NOT reimplement any fleet logic — it drives the EXISTING starfleetctl
// packages (agentbus, dashboard, task) in-process, exactly like the CLI
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

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/task"
)

//go:embed index.html
var indexFS embed.FS

// Server holds the resolved workspace root + HTTP handler.
type Server struct {
	Root   string
	Addr   string
	bus    *agentbus.Bus
	dash   *dashboard.Dashboard
	mux    *http.ServeMux
}

// New builds a web Server rooted at the given workspace root, bound to addr
// (e.g. ":8080" or "127.0.0.1:8080"). The agent-bus board identity is taken
// from the environment exactly like `agent-bus` (STARFLEET_SHIP_ID etc.).
func New(root, addr string) (*Server, error) {
	b, err := agentbus.New(root)
	if err != nil {
		return nil, fmt.Errorf("web: agent-bus: %w", err)
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
// Delegates to agentbus.Tell / broadcast — the same code path as
// `agent-bus tell` / `agent-bus broadcast`. Body via JSON or form.
func (s *Server) apiTell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	target, text, replyTo := "", "", ""
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
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
//   POST   {title, desc?, assign?, status?}  -> task capture
//   POST   {slug, ship?}                     -> task assign
//   POST   {slug}                            -> task unassign
//   POST   {slug, status}                    -> task status
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
		Assign   string `json:"assign"`   // "" | "auto"/"__auto__" | "<ship>"
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
const usage = `web [--addr <host:port>] [--no-browser] [autostart|autostart stop]
  Start the minimalist mobile-first fleet web console. Reuses the same
  in-process agentbus / dashboard / task code as the CLI subcommands, so the
  web UI and 'starfleetctl <cmd>' stay in lockstep. Defaults to
  listening on 0.0.0.0:8080 (all interfaces, so it is reachable from other
  devices — e.g. a phone on the LAN). The bus identity is taken from the environment
  (STARFLEET_SHIP_ID etc.), exactly like ` + "`agent-bus`" + `.

  Subcommands:
    autostart        Check if web server is running, start as daemon if not (for cron)
    autostart stop   Stop the web server daemon

  Examples:
    starfleetctl web                 # http://:8080
    starfleetctl web --addr 0.0.0.0:9090
    starfleetctl web autostart       # check/start (cron-friendly)
    starfleetctl web autostart stop  # stop daemon
`

// Run dispatches a `web` invocation given the resolved workspace root.
// Returns the process exit code.
func Run(root string, args []string) int {
	// Load config for default address
	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web: config:", err)
		return 1
	}
	addr := cfg.Web.ListenAddr

	if len(args) > 0 && args[0] == "autostart" {
		return RunAutostart(root, args[1:])
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		case "--addr":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "web: --addr needs a value")
				return 2
			}
			i++
			addr = args[i]
		case "--no-browser":
			// accepted for CLI parity; the server has no browser control
		default:
			fmt.Fprintf(os.Stderr, "web: unknown option: %s\n\n%s", args[i], usage)
			return 2
		}
	}
	s, err := New(root, addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web:", err)
		return 1
	}
	if err := s.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "web:", err)
		return 1
	}
	return 0
}
