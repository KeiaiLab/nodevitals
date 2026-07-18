//go:build !linux

package collector

import "context"

// NewDevProbe returns a smartProbe stub for non-Linux platforms.
// github.com/anatol/smart.go's SMART/NVMe access is ioctl-bound to Linux, so
// off-Linux discovery yields zero devices and no error — this keeps
// `go build ./...` (and this package's tests) working on macOS dev
// machines. The real probe lives in smart_probe_linux.go.
func NewDevProbe(devRoot string) smartProbe {
	return func(ctx context.Context) ([]smartDevice, error) {
		return nil, nil
	}
}
