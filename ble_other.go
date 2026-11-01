//go:build !linux

package main

import (
	"errors"
	"log/slog"
)

func newPlatformBackend(*slog.Logger) (Backend, error) {
	return nil, errors.New("only Linux (BlueZ) is supported")
}
