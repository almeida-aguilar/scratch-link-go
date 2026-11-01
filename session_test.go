package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// test client
// ---------------------------------------------------------------------------

type rpcMsg struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type client struct {
	t     *testing.T
	ws    *websocket.Conn
	next  int
	notes []rpcMsg
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() config {
	return config{Host: "device-manager.scratch.mit.edu", PollInterval: 20 * time.Millisecond}
}

func startServer(t *testing.T, b Backend) (*server, *httptest.Server) {
	t.Helper()
	srv := newServer(testConfig(), b, quietLogger())
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(func() {
		srv.closeAll()
		ts.Close()
	})
	return srv, ts
}

func dial(t *testing.T, ts *httptest.Server, path string, header http.Header) (*client, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + path
	ws, _, err := websocket.DefaultDialer.Dial(u, header)
	if err != nil {
		return nil, err
	}
	c := &client{t: t, ws: ws}
	t.Cleanup(func() { ws.Close() })
	return c, nil
}

func newClient(t *testing.T, ts *httptest.Server) *client {
	t.Helper()
	c, err := dial(t, ts, "/scratch/ble", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

func (c *client) read() rpcMsg {
	c.t.Helper()
	c.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ws.ReadMessage()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	var m rpcMsg
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("bad json %q: %v", data, err)
	}
	return m
}

// call sends a request and waits for its response, keeping notifications aside.
func (c *client) call(method string, params any) (json.RawMessage, *rpcError) {
	c.t.Helper()
	id := c.next
	c.next++
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": id}
	b, _ := json.Marshal(req)
	if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	for {
		m := c.read()
		if m.Method != "" {
			c.notes = append(c.notes, m)
			continue
		}
		return m.Result, m.Error
	}
}

func (c *client) mustCall(method string, params any) json.RawMessage {
	c.t.Helper()
	res, rerr := c.call(method, params)
	if rerr != nil {
		c.t.Fatalf("%s failed: %s", method, rerr.Message)
	}
	return res
}

// waitNote returns the next notification with the given method.
func (c *client) waitNote(method string) rpcMsg {
	c.t.Helper()
	for i, n := range c.notes {
		if n.Method == method {
			c.notes = append(c.notes[:i], c.notes[i+1:]...)
			return n
		}
	}
	for {
		m := c.read()
		if m.Method == method {
			return m
		}
		if m.Method != "" {
			c.notes = append(c.notes, m)
		}
	}
}

func (c *client) connectWeDo() {
	c.t.Helper()
	c.mustCall("discover", map[string]any{"filters": []any{map[string]any{"services": []string{wedoDevice}}}})
	c.waitNote("didDiscoverPeripheral")
	c.mustCall("connect", map[string]any{"peripheralId": wedoID})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestVersionPingAndUnknownMethod(t *testing.T) {
	_, ts := startServer(t, newFakeBackend())
	c := newClient(t, ts)

	if res := c.mustCall("ping", nil); string(res) != "42" {
		t.Errorf("ping = %s, want 42", res)
	}
	var v map[string]string
	if err := json.Unmarshal(c.mustCall("getVersion", nil), &v); err != nil || v["protocol"] == "" {
		t.Errorf("getVersion = %v, %v", v, err)
	}
	if _, rerr := c.call("nope", nil); rerr == nil || rerr.Code != -32601 {
		t.Errorf("unknown method error = %v, want code -32601", rerr)
	}
}

func TestClassicBluetoothIsRejected(t *testing.T) {
	_, ts := startServer(t, newFakeBackend())
	c, err := dial(t, ts, "/scratch/bt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := c.call("discover", map[string]any{}); rerr == nil {
		t.Error("expected an error for /scratch/bt")
	}
}

func TestWeDoSession(t *testing.T) {
	fb := newFakeBackend()
	_, ts := startServer(t, fb)
	c := newClient(t, ts)

	// discover: only the WeDo matches the service filter, not the TV.
	c.mustCall("discover", map[string]any{
		"filters":          []any{map[string]any{"services": []string{wedoDevice}}},
		"optionalServices": []string{wedoIO},
	})
	n := c.waitNote("didDiscoverPeripheral")
	var found struct {
		PeripheralID string `json:"peripheralId"`
		Name         string `json:"name"`
		RSSI         int    `json:"rssi"`
	}
	json.Unmarshal(n.Params, &found)
	if found.PeripheralID != wedoID || found.Name != "Master Box" || found.RSSI != -52 {
		t.Fatalf("unexpected discovery: %+v", found)
	}

	// connecting to a device the scan never reported must fail
	if _, rerr := c.call("connect", map[string]any{"peripheralId": "11:22:33:44:55:66"}); rerr == nil {
		t.Error("connect to an unseen device should fail")
	}
	c.mustCall("connect", map[string]any{"peripheralId": wedoID})

	// write (base64) reaches the device
	c.mustCall("write", map[string]any{"serviceId": wedoIO, "characteristicId": wedoCmdB, "message": "BgQDAAD/", "encoding": "base64"})
	got := fb.periph.char(wedoIO, wedoCmdB).writes
	if len(got) != 1 || string(got[0]) != string([]byte{6, 4, 3, 0, 0, 255}) {
		t.Fatalf("device received %v", got)
	}

	// read
	var rd struct{ Message, Encoding string }
	json.Unmarshal(c.mustCall("read", map[string]any{"serviceId": wedoDevice, "characteristicId": wedoBattery}), &rd)
	if rd.Message != "AA==" || rd.Encoding != "base64" {
		t.Errorf("read = %+v", rd)
	}

	// notifications: delivered only while subscribed, with the ids echoed as sent
	ports := fb.periph.char(wedoDevice, wedoPorts)
	if ports.emit([]byte{1}) {
		t.Fatal("notification delivered before subscribing")
	}
	c.mustCall("startNotifications", map[string]any{"serviceId": wedoDevice, "characteristicId": wedoPorts})
	if !ports.emit([]byte{3, 1, 0x33}) {
		t.Fatal("not subscribed after startNotifications")
	}
	n = c.waitNote("characteristicDidChange")
	var ch struct {
		ServiceID, CharacteristicID, Message, Encoding string
	}
	json.Unmarshal(n.Params, &ch)
	want := base64.StdEncoding.EncodeToString([]byte{3, 1, 0x33})
	if ch.ServiceID != wedoDevice || ch.CharacteristicID != wedoPorts || ch.Message != want || ch.Encoding != "base64" {
		t.Errorf("notification = %+v", ch)
	}
	c.mustCall("stopNotifications", map[string]any{"serviceId": wedoDevice, "characteristicId": wedoPorts})
	if ports.subscribed() {
		t.Error("still subscribed after stopNotifications")
	}

	// unknown characteristic is an error, not a crash
	if _, rerr := c.call("write", map[string]any{"serviceId": wedoIO, "characteristicId": "ffff", "message": "AA==", "encoding": "base64"}); rerr == nil {
		t.Error("write to unknown characteristic should fail")
	}

	// closing the WebSocket disconnects the device
	c.ws.Close()
	eventually(t, "device disconnected after the session closed", func() bool {
		select {
		case <-fb.periph.disconnected:
			return true
		default:
			return false
		}
	})
}

func TestNameFilters(t *testing.T) {
	fb := newFakeBackend()
	_, ts := startServer(t, fb)
	c := newClient(t, ts)
	c.mustCall("discover", map[string]any{"filters": []any{map[string]any{"namePrefix": "Living"}}})
	n := c.waitNote("didDiscoverPeripheral")
	var found struct{ Name string }
	json.Unmarshal(n.Params, &found)
	if found.Name != "Living room TV" {
		t.Errorf("namePrefix filter matched %q", found.Name)
	}
}

func TestDeviceDropClosesSession(t *testing.T) {
	fb := newFakeBackend()
	_, ts := startServer(t, fb)
	c := newClient(t, ts)
	c.connectWeDo()

	fb.periph.connected.Store(false) // hub turned off
	c.ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ws.ReadMessage(); err == nil {
		t.Fatal("expected the WebSocket to close when the device drops")
	}
}

func TestTwoSessionsDoNotInterfere(t *testing.T) {
	fb := newFakeBackend()
	_, ts := startServer(t, fb)
	a, b := newClient(t, ts), newClient(t, ts)
	a.connectWeDo()
	// the second session can still scan and see the device
	b.mustCall("discover", map[string]any{"filters": []any{map[string]any{"services": []string{wedoDevice}}}})
	b.waitNote("didDiscoverPeripheral")
}

func TestOriginCheck(t *testing.T) {
	_, ts := startServer(t, newFakeBackend())
	for origin, ok := range map[string]bool{
		"https://scratch.mit.edu":              true,
		"https://www.turbowarp.org":            true,
		"https://evil.example":                 false,
		"https://scratch.mit.edu.evil.example": false,
	} {
		_, err := dial(t, ts, "/scratch/ble", http.Header{"Origin": []string{origin}})
		if (err == nil) != ok {
			t.Errorf("origin %s: accepted=%v, want %v", origin, err == nil, ok)
		}
	}
}

// TestReplayRealWeDoLog replays the client side of a real Scratch <-> WeDo 2.0
// session (testdata/wedo2-session.log) and checks every request succeeds.
func TestReplayRealWeDoLog(t *testing.T) {
	f, err := os.Open("testdata/wedo2-session.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	fb := newFakeBackend()
	_, ts := startServer(t, fb)
	c := newClient(t, ts)

	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		_, payload, ok := strings.Cut(sc.Text(), " <- ")
		if !ok {
			continue
		}
		var req struct {
			Method string
			Params json.RawMessage
		}
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			t.Fatalf("bad line %q: %v", payload, err)
		}
		var params any
		json.Unmarshal(req.Params, &params)
		c.mustCall(req.Method, params)
		if req.Method == "discover" {
			c.waitNote("didDiscoverPeripheral") // as Scratch does before connecting
		}
		count++
	}
	if count < 10 {
		t.Fatalf("replayed only %d requests", count)
	}
	if !fb.periph.char(wedoIO, wedoSensor).subscribed() {
		t.Error("sensor characteristic should be subscribed after the replay")
	}
}
