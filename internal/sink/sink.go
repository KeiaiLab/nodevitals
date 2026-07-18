// Package sink delivers events and samples to destinations.
package sink

import (
	"context"

	"github.com/nodevitals/nodevitals/internal/model"
)

// Sink delivers events to one destination.
type Sink interface {
	Name() string
	EmitEvents(ctx context.Context, events []model.Event) error
}
