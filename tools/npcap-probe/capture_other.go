//go:build !windows || !amd64

package main

import (
	"context"
	"fmt"
)

func capture(ctx context.Context, o options) error {
	return fmt.Errorf("this Npcap probe currently supports Windows amd64 only")
}
