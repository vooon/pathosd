package checks

import (
	"context"
	"time"
)

type Result struct {
	Success  bool          `json:"success"`
	Detail   string        `json:"detail"`
	Duration time.Duration `json:"duration"`
	Err      error         `json:"-"`
	TimedOut bool          `json:"timed_out"`
}

type Checker interface {
	Check(ctx context.Context) Result
	Type() string
}
