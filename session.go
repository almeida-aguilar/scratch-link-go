package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// session is one WebSocket connection from Scratch. Requests are handled one
// at a time, in order, like the original Scratch Link does.
type session struct {
	srv  *server
	ws   *websocket.Conn
	wmu  sync.Mutex // gorilla/websocket allows one concurrent writer
	kind string     // "ble" or "bt"
	log  *slog.Logger

	mu         sync.Mutex
	seen       map[string]time.Time // devices reported by the scan (id -> last report)
	filters    []compiledFilter
	scanCancel context.CancelFunc
	scanDone   chan struct{}
	periph     Peripheral
	devID      string
	notifying  map[string]Characteristic // "service/char" -> subscribed characteristic

	done chan struct{}
	once sync.Once
}

func newSession(srv *server, ws *websocket.Conn, kind string, log *slog.Logger) *session {
	return &session{
		srv:       srv,
		ws:        ws,
		kind:      kind,
		log:       log,
		seen:      map[string]time.Time{},
		notifying: map[string]Characteristic{},
		done:      make(chan struct{}),
	}
}

func (s *session) readLoop() {
	for {
		_, data, err := s.ws.ReadMessage()
		if err != nil {
			s.log.Debug("read ended", "err", err)
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(data, &req); err != nil {
			s.log.Debug("ignoring malformed message", "err", err)
			continue
		}
		s.log.Debug("request", "method", req.Method, "params", string(req.Params))
		result, rerr := s.dispatch(req)
		if req.isNotification() {
			continue
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			s.log.Warn("request failed", "method", req.Method, "err", rerr.Message)
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		s.send(resp)
	}
}

func (s *session) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := s.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		s.log.Debug("write failed", "err", err)
	}
}

func (s *session) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		s.stopScan()

		s.mu.Lock()
		periph, devID := s.periph, s.devID
		chars := make([]Characteristic, 0, len(s.notifying))
		for _, c := range s.notifying {
			chars = append(chars, c)
		}
		s.periph = nil
		s.notifying = map[string]Characteristic{}
		s.mu.Unlock()

		for _, c := range chars {
			c.Notify(nil)
		}
		if periph != nil {
			s.log.Info("disconnecting", "device", devID)
			periph.Disconnect()
		}
		s.ws.Close()
		s.srv.remove(s)
		s.log.Info("session ended")
	})
}

func (s *session) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "ping":
		return 42, nil
	case "getVersion":
		return map[string]string{"protocol": "1.3", "version": version}, nil
	}
	if s.kind != "ble" {
		return nil, errf("classic Bluetooth is not supported (BLE only)")
	}
	switch req.Method {
	case "discover":
		return s.discover(req.Params)
	case "connect":
		return s.connect(req.Params)
	case "getServices":
		return s.getServices()
	case "read":
		return s.read(req.Params)
	case "write":
		return s.write(req.Params)
	case "startNotifications":
		return s.setNotify(req.Params, true)
	case "stopNotifications":
		return s.setNotify(req.Params, false)
	}
	return nil, &rpcError{Code: -32601, Message: "unknown method: " + req.Method}
}

// ---------------------------------------------------------------------------
// discover / connect
// ---------------------------------------------------------------------------

func (s *session) discover(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Filters []filterParam `json:"filters"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errf("invalid parameters: %v", err)
	}
	filters, err := compileFilters(p.Filters)
	if err != nil {
		return nil, errf("invalid filter: %v", err)
	}
	s.mu.Lock()
	connected := s.periph != nil
	s.mu.Unlock()
	if connected {
		return nil, errf("a device is already connected in this session")
	}

	s.stopScan() // a new discover replaces the previous one
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.mu.Lock()
	s.filters = filters
	s.scanCancel, s.scanDone = cancel, done
	s.mu.Unlock()

	s.log.Info("scanning", "filters", len(filters))
	go func() {
		defer close(done)
		if err := s.srv.backend.Scan(ctx, s.onAdvert); err != nil && ctx.Err() == nil {
			s.log.Error("scan failed", "err", err)
		}
	}()
	return nil, nil
}

func (s *session) stopScan() {
	s.mu.Lock()
	cancel, done := s.scanCancel, s.scanDone
	s.scanCancel, s.scanDone = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (s *session) onAdvert(a Advert) {
	select {
	case <-s.done:
		return
	default:
	}
	s.mu.Lock()
	if s.scanCancel == nil || !matchAny(s.filters, a) {
		s.mu.Unlock()
		return
	}
	last, known := s.seen[a.ID]
	if known && time.Since(last) < 700*time.Millisecond {
		s.mu.Unlock()
		return
	}
	s.seen[a.ID] = time.Now()
	s.mu.Unlock()

	s.log.Debug("device found", "id", a.ID, "name", a.Name, "rssi", a.RSSI)
	s.notify("didDiscoverPeripheral", map[string]any{
		"peripheralId": a.ID,
		"name":         a.Name,
		"rssi":         a.RSSI,
	})
}

func (s *session) connect(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		PeripheralID json.RawMessage `json:"peripheralId"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errf("invalid parameters: %v", err)
	}
	id := strings.Trim(string(p.PeripheralID), `"`)

	s.mu.Lock()
	_, seen := s.seen[id]
	connected := s.periph != nil
	s.mu.Unlock()
	if connected {
		return nil, errf("already connected")
	}
	if !seen {
		return nil, errf("unknown device: %s", id)
	}

	s.stopScan()
	s.log.Info("connecting", "device", id)
	periph, err := s.srv.backend.Connect(id)
	if err != nil {
		return nil, errf("could not connect: %v", err)
	}
	s.mu.Lock()
	s.periph, s.devID = periph, id
	s.mu.Unlock()

	select {
	case <-s.done: // the session closed while we were connecting
		periph.Disconnect()
		return nil, errf("session closed")
	default:
	}
	s.log.Info("connected", "device", id, "services", len(periph.Services()))
	go s.watch(periph)
	return nil, nil
}

// watch closes the session when the device drops the connection (turned off,
// out of range). BlueZ does not push that event through the Go library.
func (s *session) watch(p Peripheral) {
	t := time.NewTicker(s.srv.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if !p.Connected() {
				s.log.Info("device disconnected")
				s.close()
				return
			}
		}
	}
}

func (s *session) getServices() (any, *rpcError) {
	s.mu.Lock()
	p := s.periph
	s.mu.Unlock()
	if p == nil {
		return nil, errf("not connected")
	}
	return p.Services(), nil
}

// ---------------------------------------------------------------------------
// read / write / notifications
// ---------------------------------------------------------------------------

func (s *session) lookup(p charParams) (Characteristic, string, *rpcError) {
	svc, err := parseUUIDParam(p.ServiceID)
	if err != nil {
		return nil, "", errf("invalid serviceId: %v", err)
	}
	chr, err := parseUUIDParam(p.CharacteristicID)
	if err != nil {
		return nil, "", errf("invalid characteristicId: %v", err)
	}
	s.mu.Lock()
	periph := s.periph
	s.mu.Unlock()
	if periph == nil {
		return nil, "", errf("not connected")
	}
	c, ok := periph.Characteristic(svc, chr)
	if !ok {
		return nil, "", errf("characteristic not found: %s / %s", svc, chr)
	}
	return c, svc + "/" + chr, nil
}

func (s *session) read(raw json.RawMessage) (any, *rpcError) {
	var p charParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errf("invalid parameters: %v", err)
	}
	c, _, rerr := s.lookup(p)
	if rerr != nil {
		return nil, rerr
	}
	data, err := c.Read()
	if err != nil {
		return nil, errf("read failed: %v", err)
	}
	if p.StartNotifications {
		if _, rerr := s.setNotify(raw, true); rerr != nil {
			return nil, rerr
		}
	}
	return map[string]any{
		"message":  base64.StdEncoding.EncodeToString(data),
		"encoding": "base64",
	}, nil
}

func (s *session) write(raw json.RawMessage) (any, *rpcError) {
	var p charParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errf("invalid parameters: %v", err)
	}
	c, _, rerr := s.lookup(p)
	if rerr != nil {
		return nil, rerr
	}
	data := []byte(p.Message)
	if p.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(p.Message)
		if err != nil {
			return nil, errf("invalid base64: %v", err)
		}
		data = b
	}
	withResponse := p.WithResponse != nil && *p.WithResponse
	if err := c.Write(data, withResponse); err != nil {
		return nil, errf("write failed: %v", err)
	}
	return len(data), nil
}

func (s *session) setNotify(raw json.RawMessage, on bool) (any, *rpcError) {
	var p charParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errf("invalid parameters: %v", err)
	}
	c, key, rerr := s.lookup(p)
	if rerr != nil {
		return nil, rerr
	}
	s.mu.Lock()
	_, active := s.notifying[key]
	s.mu.Unlock()
	if active == on {
		return nil, nil
	}

	if on {
		svcID, chrID := p.ServiceID, p.CharacteristicID
		err := c.Notify(func(b []byte) {
			select {
			case <-s.done:
				return
			default:
			}
			s.log.Log(context.Background(), levelTrace, "notification", "char", key, "bytes", len(b))
			s.notify("characteristicDidChange", map[string]any{
				"serviceId":        svcID,
				"characteristicId": chrID,
				"encoding":         "base64",
				"message":          base64.StdEncoding.EncodeToString(b),
			})
		})
		if err != nil {
			return nil, errf("could not enable notifications: %v", err)
		}
		s.mu.Lock()
		s.notifying[key] = c
		s.mu.Unlock()
		return nil, nil
	}

	if err := c.Notify(nil); err != nil {
		return nil, errf("could not disable notifications: %v", err)
	}
	s.mu.Lock()
	delete(s.notifying, key)
	s.mu.Unlock()
	return nil, nil
}
