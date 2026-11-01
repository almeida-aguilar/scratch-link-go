package main

import (
	"context"
	"errors"
	"sync"
)

// scanMux lets several sessions share the single scan BlueZ allows.
//
// The underlying scan (run) starts with the first subscriber and stops with
// the last one. Advertisements seen so far are replayed to late subscribers,
// because BlueZ only reports a device again when its properties change.
type scanMux struct {
	run func(ctx context.Context, emit func(Advert)) error

	life    sync.Mutex // serializes start/stop of the underlying scan
	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	mu    sync.Mutex // guards subs, known, dead
	subs  map[*scanSub]struct{}
	known map[string]Advert
	dead  bool // run ended by itself (error), needs a restart
}

type scanSub struct {
	fn   func(Advert)
	errc chan error
}

func newScanMux(run func(ctx context.Context, emit func(Advert)) error) *scanMux {
	return &scanMux{run: run, subs: map[*scanSub]struct{}{}, known: map[string]Advert{}}
}

// Scan subscribes fn and blocks until ctx is cancelled (returns nil) or the
// underlying scan fails (returns the error).
func (m *scanMux) Scan(ctx context.Context, fn func(Advert)) error {
	sub := &scanSub{fn: fn, errc: make(chan error, 1)}

	m.life.Lock()
	m.mu.Lock()
	dead := m.dead
	m.mu.Unlock()
	if m.running && dead {
		m.stopLocked()
	}
	m.mu.Lock()
	m.subs[sub] = struct{}{}
	replay := make([]Advert, 0, len(m.known))
	for _, a := range m.known {
		replay = append(replay, a)
	}
	m.mu.Unlock()
	if !m.running {
		m.startLocked()
	}
	m.life.Unlock()

	for _, a := range replay {
		fn(a)
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-sub.errc:
	}

	m.life.Lock()
	m.mu.Lock()
	delete(m.subs, sub)
	empty := len(m.subs) == 0
	m.mu.Unlock()
	if empty && m.running {
		m.stopLocked()
	}
	m.life.Unlock()
	return err
}

func (m *scanMux) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.cancel, m.done, m.running = cancel, done, true
	m.mu.Lock()
	m.known = map[string]Advert{}
	m.dead = false
	m.mu.Unlock()
	go func() {
		err := m.run(ctx, m.emit)
		m.mu.Lock()
		if ctx.Err() == nil { // ended on its own, not because we stopped it
			m.dead = true
			if err == nil {
				err = errors.New("scan ended unexpectedly")
			}
			for s := range m.subs {
				select {
				case s.errc <- err:
				default:
				}
			}
		}
		m.mu.Unlock()
		close(done)
	}()
}

func (m *scanMux) stopLocked() {
	m.cancel()
	<-m.done
	m.running = false
	m.mu.Lock()
	m.known = map[string]Advert{}
	m.dead = false
	m.mu.Unlock()
}

func (m *scanMux) emit(a Advert) {
	m.mu.Lock()
	m.known[a.ID] = a
	fns := make([]func(Advert), 0, len(m.subs))
	for s := range m.subs {
		fns = append(fns, s.fn)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn(a)
	}
}
