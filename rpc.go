package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// JSON-RPC 2.0 as spoken by Scratch (scratch-vm / scratch-link protocol).

type rpcRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     json.RawMessage `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

func errf(format string, args ...any) *rpcError {
	return &rpcError{Code: -32000, Message: fmt.Sprintf(format, args...)}
}

// isNotification reports whether the request has no id (no reply expected).
func (r rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// charParams are the parameters shared by read, write, startNotifications and
// stopNotifications. The raw ids are kept so they can be echoed back exactly
// as Scratch sent them.
type charParams struct {
	ServiceID          json.RawMessage `json:"serviceId"`
	CharacteristicID   json.RawMessage `json:"characteristicId"`
	Message            string          `json:"message"`
	Encoding           string          `json:"encoding"`
	WithResponse       *bool           `json:"withResponse"`
	StartNotifications bool            `json:"startNotifications"`
}

// parseUUIDParam accepts a JSON string ("180f", "0x180f", full UUID) or a JSON
// number (some Scratch extensions send 16-bit UUIDs as integers).
func parseUUIDParam(raw json.RawMessage) (string, error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "", errors.New("missing UUID")
	}
	if t[0] != '"' {
		n, err := strconv.ParseUint(t, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid numeric UUID %q", t)
		}
		return fmt.Sprintf("%08x%s", n, baseUUIDSuffix), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return normalizeUUID(s)
}

// filterParam is one entry of the "filters" list of a discover request.
type filterParam struct {
	Services   []json.RawMessage `json:"services"`
	Name       string            `json:"name"`
	NamePrefix string            `json:"namePrefix"`
}

type compiledFilter struct {
	services   []string
	name       string
	namePrefix string
}

func compileFilters(in []filterParam) ([]compiledFilter, error) {
	out := make([]compiledFilter, 0, len(in))
	for _, f := range in {
		cf := compiledFilter{name: f.Name, namePrefix: f.NamePrefix}
		for _, raw := range f.Services {
			u, err := parseUUIDParam(raw)
			if err != nil {
				return nil, err
			}
			cf.services = append(cf.services, u)
		}
		out = append(out, cf)
	}
	return out, nil
}

func (f compiledFilter) matches(a Advert) bool {
	for _, want := range f.services {
		found := false
		for _, have := range a.Services {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.name != "" && a.Name != f.name {
		return false
	}
	if f.namePrefix != "" && !strings.HasPrefix(a.Name, f.namePrefix) {
		return false
	}
	return true
}

// matchAny: a device matches if it satisfies any filter (or there are none).
func matchAny(fs []compiledFilter, a Advert) bool {
	if len(fs) == 0 {
		return true
	}
	for _, f := range fs {
		if f.matches(a) {
			return true
		}
	}
	return false
}
