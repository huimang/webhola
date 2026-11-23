//go:build (!windows && !linux && !darwin) || (windows && !amd64)

package capture

import (
	"context"
	"fmt"
	"runtime"
)

type unsupportedAdapter struct{}

func NewAdapter() CaptureAdapter { return unsupportedAdapter{} }
func (unsupportedAdapter) Interfaces(context.Context) ([]CaptureInterface, error) {
	return nil, fmt.Errorf("live capture is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}
func (unsupportedAdapter) Open(context.Context, CaptureConfig) (PacketSource, error) {
	return nil, fmt.Errorf("live capture is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
}
