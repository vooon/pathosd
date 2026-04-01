package checks

import (
	"fmt"

	"github.com/vooon/pathosd/internal/config"
)

func NewChecker(cfg *config.CheckConfig) (Checker, error) {
	switch cfg.Type {
	case "http":
		if cfg.HTTP == nil {
			return nil, fmt.Errorf("http check config is nil")
		}
		return NewHTTPChecker(cfg.HTTP), nil
	case "dns":
		if cfg.DNS == nil {
			return nil, fmt.Errorf("dns check config is nil")
		}
		return NewDNSChecker(cfg.DNS), nil
	case "ping":
		if cfg.Ping == nil {
			return nil, fmt.Errorf("ping check config is nil")
		}
		return NewPingChecker(cfg.Ping), nil
	default:
		return nil, fmt.Errorf("unsupported check type %q", cfg.Type)
	}
}
