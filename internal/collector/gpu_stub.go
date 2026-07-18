//go:build !gpu

package collector

import "context"

// nvmlUnavailable is the stub gpuReader for builds without the `gpu` tag: the
// gpu tier requires the cgo/NVML binary (:v-gpu image). Read yields nothing,
// XidEvents is an already-closed channel (so NewGPUCollector's drain exits at
// once), Close is a no-op.
type nvmlUnavailable struct{ closed chan xidRaw }

// NewNVMLReader returns the stub reader. The gpu-tagged build replaces this
// with the real NVML-backed implementation.
func NewNVMLReader() (gpuReader, error) {
	ch := make(chan xidRaw)
	close(ch)
	return nvmlUnavailable{closed: ch}, nil
}

func (n nvmlUnavailable) Read(context.Context) ([]gpuDevice, error) { return nil, nil }
func (n nvmlUnavailable) XidEvents() <-chan xidRaw                  { return n.closed }
func (n nvmlUnavailable) Close() error                              { return nil }
