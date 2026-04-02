package policy

import (
	"github.com/vooon/pathosd/internal/config"
	"github.com/vooon/pathosd/internal/model"
)

func Evaluate(healthy bool, lowerPriorityFilePresent bool, p *config.PolicyConfig) model.VIPState {
	if healthy {
		if lowerPriorityFilePresent {
			return model.StatePessimized
		}
		return model.StateAnnounced
	}
	switch p.FailAction {
	case "withdraw":
		return model.StateWithdrawn
	case "lower_priority":
		return model.StatePessimized
	default:
		return model.StateWithdrawn
	}
}
