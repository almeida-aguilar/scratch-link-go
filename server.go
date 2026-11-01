package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// server serves the Scratch Link endpoints. It is independent of TLS and of
// the Bluetooth stack, which makes it easy to test.
type server struct {
	cfg     config
	backend Backend
	log     *slog.Logger

	upgrader websocket.Upgrader
	nextID   atomic.Int64

	mu       sync.Mutex
	sessions map[*session]struct{}
}

func newServer(cfg config, backend Backend, log *slog.Logger) *server {
	s := &server{cfg: cfg, backend: backend, log: log, sessions: map[*session]struct{}{}}
	s.upgrader = websocket.Upgrader{CheckOrigin: s.originOK}
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/scratch/ble", func(w http.ResponseWriter, r *http.Request) { s.serveWS(w, r, "ble") })
	mux.HandleFunc("/scratch/bt", func(w http.ResponseWriter, r *http.Request) { s.serveWS(w, r, "bt") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "scratch-link-go %s is running.\nIf you can read this, your browser trusts the certificate. Go back to Scratch.\n", version)
	})
	return mux
}

func (s *server) serveWS(w http.ResponseWriter, r *http.Request, kind string) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Debug("websocket upgrade failed", "err", err)
		return
	}
	log := s.log.With("session", s.nextID.Add(1), "kind", kind)
	sess := newSession(s, ws, kind, log)
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
	log.Info("session started", "remote", r.RemoteAddr)
	defer sess.close()
	sess.readLoop()
}

func (s *server) remove(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

// closeAll ends every session (used on shutdown so devices get disconnected).
func (s *server) closeAll() {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		all = append(all, sess)
	}
	s.mu.Unlock()
	for _, sess := range all {
		sess.close()
	}
}

var defaultOrigins = []string{"scratch.mit.edu", "turbowarp.org", "localhost", "127.0.0.1"}

// originOK stops arbitrary web pages from driving your Bluetooth devices.
func (s *server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || s.cfg.AnyOrigin {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, a := range append(append([]string{}, defaultOrigins...), s.cfg.Origins...) {
		if h == a || strings.HasSuffix(h, "."+a) {
			return true
		}
	}
	s.log.Warn("origin rejected; use -origins to allow it", "origin", o)
	return false
}
