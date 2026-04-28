package checks

import (
	"fmt"

	"github.com/vooon/pathosd/internal/config"
)

func NewChecker(cfg *config.CheckConfig) (Checker, error) {
	switch cfg.Type {
	case config.CheckTypeHTTP:
		if cfg.HTTP == nil {
			return nil, fmt.Errorf("http check config is nil")
		}
		return NewHTTPChecker(cfg.HTTP)
	case config.CheckTypeDNS:
		if cfg.DNS == nil {
			return nil, fmt.Errorf("dns check config is nil")
		}
		return NewDNSChecker(cfg.DNS), nil
	case config.CheckTypePing:
		if cfg.Ping == nil {
			return nil, fmt.Errorf("ping check config is nil")
		}
		return NewPingChecker(cfg.Ping), nil
	case config.CheckTypeUDP:
		if cfg.UDP == nil {
			return nil, fmt.Errorf("udp check config is nil")
		}
		return NewUDPChecker(cfg.UDP), nil
	default:
		return nil, fmt.Errorf("unsupported check type %q", cfg.Type)
	}
}
