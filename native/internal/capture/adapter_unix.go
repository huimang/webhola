//go:build linux || darwin

package capture

import (
	"context"
	"net"
)

func systemInterfaces(ctx context.Context) ([]CaptureInterface, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]CaptureInterface, 0, len(ifs))
	for _, i := range ifs {
		out = append(out, CaptureInterface{ID: i.Name, Name: i.Name, IsLoopback: i.Flags&net.FlagLoopback != 0})
	}
	return out, nil
}
