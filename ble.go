package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Advert is one BLE advertisement as seen during a scan.
type Advert struct {
	ID       string   // stable identifier of the device (its MAC address)
	Name     string   // local name, may be empty
	RSSI     int      // signal strength in dBm, 0 if unknown
	Services []string // advertised service UUIDs, normalized (see normalizeUUID)
}

// Backend is everything the protocol layer needs from the Bluetooth stack.
// The real implementation talks to BlueZ (ble_bluez.go); tests use a fake.
type Backend interface {
	// Scan reports advertisements through fn until ctx is cancelled.
	// It blocks, and may be called concurrently by several sessions.
	Scan(ctx context.Context, fn func(Advert)) error
	// Connect connects to a device previously reported by Scan.
	Connect(id string) (Peripheral, error)
}

// Peripheral is a connected BLE device with its GATT tree already discovered.
type Peripheral interface {
	Services() []string
	Characteristic(service, char string) (Characteristic, bool)
	Connected() bool
	Disconnect() error
}

// Characteristic is one GATT characteristic.
type Characteristic interface {
	Read() ([]byte, error)
	Write(data []byte, withResponse bool) error
	// Notify subscribes fn to value changes. A nil fn unsubscribes.
	Notify(fn func([]byte)) error
}

const baseUUIDSuffix = "-0000-1000-8000-00805f9b34fb"

// normalizeUUID converts any common UUID spelling ("180f", "0x180F",
// "0000180f-0000-1000-8000-00805f9b34fb", 32 hex digits without dashes) to
// the lowercase 128-bit dashed form, so UUIDs can be compared as strings.
func normalizeUUID(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "0x")
	if !strings.Contains(s, "-") && len(s) <= 8 {
		n, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return "", fmt.Errorf("invalid UUID %q", s)
		}
		return fmt.Sprintf("%08x%s", n, baseUUIDSuffix), nil
	}
	c := strings.ReplaceAll(s, "-", "")
	if _, err := hex.DecodeString(c); err != nil || len(c) != 32 {
		return "", fmt.Errorf("invalid UUID %q", s)
	}
	return c[:8] + "-" + c[8:12] + "-" + c[12:16] + "-" + c[16:20] + "-" + c[20:], nil
}
