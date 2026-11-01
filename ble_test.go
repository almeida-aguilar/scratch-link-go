package main

import (
	"encoding/json"
	"testing"
)

func TestNormalizeUUID(t *testing.T) {
	full := "0000180f-0000-1000-8000-00805f9b34fb"
	for in, want := range map[string]string{
		"180f":                                 full,
		"0x180F":                               full,
		"0000180F-0000-1000-8000-00805F9B34FB": full,
		"0000180f00001000800000805f9b34fb":     full,
		"00001523-1212-EFDE-1523-785FEABCD123": "00001523-1212-efde-1523-785feabcd123",
	} {
		got, err := normalizeUUID(in)
		if err != nil || got != want {
			t.Errorf("normalizeUUID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "zz", "1234567890", "0000180f-0000-1000-8000"} {
		if _, err := normalizeUUID(bad); err == nil {
			t.Errorf("normalizeUUID(%q) should fail", bad)
		}
	}
}

func TestParseUUIDParamAcceptsNumbers(t *testing.T) {
	got, err := parseUUIDParam(json.RawMessage(`61455`)) // 0xf00f
	if err != nil || got != "0000f00f"+baseUUIDSuffix {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := parseUUIDParam(json.RawMessage(`null`)); err == nil {
		t.Error("null should fail")
	}
}
