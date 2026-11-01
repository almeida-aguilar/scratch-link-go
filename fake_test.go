package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// WeDo 2.0 UUIDs (from a real session).
const (
	wedoDevice  = "00001523-1212-efde-1523-785feabcd123"
	wedoIO      = "00004f0e-1212-efde-1523-785feabcd123"
	wedoPorts   = "00001527-1212-efde-1523-785feabcd123"
	wedoBattery = "00001528-1212-efde-1523-785feabcd123"
	wedoSensor  = "00001560-1212-efde-1523-785feabcd123"
	wedoCmdA    = "00001563-1212-efde-1523-785feabcd123"
	wedoCmdB    = "00001565-1212-efde-1523-785feabcd123"

	wedoID = "C2:01:06:01:7C:55"
)

type fakeChar struct {
	mu       sync.Mutex
	value    []byte
	writes   [][]byte
	notifyFn func([]byte)
}

func (c *fakeChar) Read() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.value...), nil
}

func (c *fakeChar) Write(data []byte, withResponse bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	return nil
}

func (c *fakeChar) Notify(fn func([]byte)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifyFn = fn
	return nil
}

func (c *fakeChar) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.notifyFn != nil
}

// emit simulates the device sending a notification. Reports whether anyone was subscribed.
func (c *fakeChar) emit(data []byte) bool {
	c.mu.Lock()
	fn := c.notifyFn
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	fn(data)
	return true
}

type fakePeripheral struct {
	services     []string
	chars        map[string]*fakeChar
	connected    atomic.Bool
	disconnected chan struct{}
	once         sync.Once
}

func newWeDoPeripheral() *fakePeripheral {
	p := &fakePeripheral{
		services:     []string{wedoDevice, wedoIO},
		chars:        map[string]*fakeChar{},
		disconnected: make(chan struct{}),
	}
	for _, k := range []string{
		wedoDevice + "/" + wedoPorts,
		wedoDevice + "/" + wedoBattery,
		wedoIO + "/" + wedoSensor,
		wedoIO + "/" + wedoCmdA,
		wedoIO + "/" + wedoCmdB,
	} {
		p.chars[k] = &fakeChar{}
	}
	p.chars[wedoDevice+"/"+wedoBattery].value = []byte{0}
	p.connected.Store(true)
	return p
}

func (p *fakePeripheral) char(svc, chr string) *fakeChar { return p.chars[svc+"/"+chr] }
func (p *fakePeripheral) Services() []string             { return p.services }
func (p *fakePeripheral) Connected() bool                { return p.connected.Load() }

func (p *fakePeripheral) Characteristic(svc, chr string) (Characteristic, bool) {
	c, ok := p.chars[svc+"/"+chr]
	if !ok {
		return nil, false
	}
	return c, true
}

func (p *fakePeripheral) Disconnect() error {
	p.connected.Store(false)
	p.once.Do(func() { close(p.disconnected) })
	return nil
}

type fakeBackend struct {
	adverts []Advert
	periph  *fakePeripheral

	mu         sync.Mutex
	connectIDs []string
}

// Scan reports the canned adverts immediately, then waits for ctx.
func (f *fakeBackend) Scan(ctx context.Context, fn func(Advert)) error {
	for _, a := range f.adverts {
		fn(a)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeBackend) Connect(id string) (Peripheral, error) {
	f.mu.Lock()
	f.connectIDs = append(f.connectIDs, id)
	f.mu.Unlock()
	if f.periph == nil || id != wedoID {
		return nil, errors.New("fake: no such device")
	}
	return f.periph, nil
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		adverts: []Advert{
			{ID: "AA:BB:CC:DD:EE:01", Name: "Living room TV", RSSI: -40, Services: []string{"0000180f" + baseUUIDSuffix}},
			{ID: wedoID, Name: "Master Box", RSSI: -52, Services: []string{wedoDevice}},
		},
		periph: newWeDoPeripheral(),
	}
}
