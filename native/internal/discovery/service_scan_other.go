//go:build !windows

package discovery

import (
	"context"

	i18npkg "webhola/native/internal/i18n"
)

func ListListeners(context.Context) ([]Listener, error) {
	return nil, i18npkg.MessageError("service_scan_other.automatic_http_service_discovery_currently_supports_windows")
}

func LocalAdapters() ([]LocalAdapter, error) { return nil, nil }
