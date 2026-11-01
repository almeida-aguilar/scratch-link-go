//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// maxRead is the buffer size for characteristic reads.
const maxRead = 512

// bluezBackend implements Backend on top of BlueZ via tinygo.org/x/bluetooth.
type bluezBackend struct {
	adapter *bluetooth.Adapter
	mux     *scanMux
	log     *slog.Logger

	mu    sync.Mutex
	addrs map[string]bluetooth.Address
}

func newPlatformBackend(log *slog.Logger) (Backend, error) {
	a := bluetooth.DefaultAdapter
	if err := a.Enable(); err != nil {
		return nil, fmt.Errorf("cannot use the Bluetooth adapter (is bluetoothd running? are you in a container?): %w", err)
	}
	b := &bluezBackend{adapter: a, log: log, addrs: map[string]bluetooth.Address{}}
	b.mux = newScanMux(b.runScan)
	return b, nil
}

func (b *bluezBackend) Scan(ctx context.Context, fn func(Advert)) error {
	return b.mux.Scan(ctx, fn)
}

// runScan is the single underlying scan shared by all sessions.
func (b *bluezBackend) runScan(ctx context.Context, emit func(Advert)) error {
	errc := make(chan error, 1)
	go func() {
		errc <- b.adapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
			emit(b.advert(r))
		})
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// StopScan can fail if the scan has not fully started yet: retry.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.adapter.StopScan()
		select {
		case <-errc:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("the scan did not stop in time")
}

func (b *bluezBackend) advert(r bluetooth.ScanResult) Advert {
	id := r.Address.MAC.String()
	b.mu.Lock()
	b.addrs[id] = r.Address
	b.mu.Unlock()
	a := Advert{ID: id, Name: r.LocalName(), RSSI: int(r.RSSI)}
	for _, u := range r.ServiceUUIDs() {
		if n, err := normalizeUUID(u.String()); err == nil {
			a.Services = append(a.Services, n)
		}
	}
	return a
}

func (b *bluezBackend) Connect(id string) (Peripheral, error) {
	b.mu.Lock()
	addr, ok := b.addrs[id]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown device %s", id)
	}
	dev, err := b.adapter.Connect(addr, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, err
	}
	services, err := dev.DiscoverServices(nil)
	if err != nil {
		dev.Disconnect()
		return nil, fmt.Errorf("discovering services: %w", err)
	}
	p := &bluezPeripheral{dev: dev, chars: map[string]*bluetooth.DeviceCharacteristic{}}
	for _, svc := range services {
		sid, err := normalizeUUID(svc.UUID().String())
		if err != nil {
			continue
		}
		p.services = append(p.services, sid)
		cs, err := svc.DiscoverCharacteristics(nil)
		if err != nil {
			dev.Disconnect()
			return nil, fmt.Errorf("discovering characteristics: %w", err)
		}
		for i := range cs {
			c := cs[i]
			cid, err := normalizeUUID(c.UUID().String())
			if err != nil {
				continue
			}
			p.chars[sid+"/"+cid] = &c
		}
	}
	return p, nil
}

type bluezPeripheral struct {
	dev      bluetooth.Device
	services []string
	chars    map[string]*bluetooth.DeviceCharacteristic
}

func (p *bluezPeripheral) Services() []string { return p.services }

func (p *bluezPeripheral) Characteristic(service, char string) (Characteristic, bool) {
	c, ok := p.chars[service+"/"+char]
	if !ok {
		return nil, false
	}
	return bluezChar{c}, true
}

func (p *bluezPeripheral) Connected() bool {
	ok, err := p.dev.Connected()
	return err != nil || ok // on a D-Bus error, do not assume it dropped
}

func (p *bluezPeripheral) Disconnect() error { return p.dev.Disconnect() }

type bluezChar struct {
	c *bluetooth.DeviceCharacteristic
}

func (c bluezChar) Read() ([]byte, error) {
	buf := make([]byte, maxRead)
	n, err := c.c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Write falls back to the other write mode if the preferred one is rejected.
func (c bluezChar) Write(data []byte, withResponse bool) error {
	var err error
	if withResponse {
		if _, err = c.c.Write(data); err != nil {
			_, err = c.c.WriteWithoutResponse(data)
		}
		return err
	}
	if _, err = c.c.WriteWithoutResponse(data); err != nil {
		_, err = c.c.Write(data)
	}
	return err
}

func (c bluezChar) Notify(fn func([]byte)) error {
	if fn == nil {
		return c.c.EnableNotifications(nil)
	}
	return c.c.EnableNotifications(fn)
}
