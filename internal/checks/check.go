package checks

import (
	"context"
	"time"

	"github.com/vooon/pathosd/internal/model"
)

type Result struct {
	Success  bool          `json:"success"`
	Detail   string        `json:"detail"`
	Duration time.Duration `json:"duration"`
	Err      error         `json:"-"`
	TimedOut bool          `json:"timed_out,omitempty"`
	// Details holds check-type-specific observations (status code, RTTs,
	// rcode, ...) when the backend got far enough to produce them.
	Details model.CheckDetails `json:"details,omitempty"`
}

type Checker interface {
	Check(ctx context.Context) Result
	Type() string
}
