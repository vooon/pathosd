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

// defaultLowerPriorityPrepend is used when the lower_priority block carries no
// explicit as_path_prepend (config defaults normally fill it in).
const defaultLowerPriorityPrepend = 6

// RouteAttrsFor returns the BGP attributes to originate the VIP with in the
// given state. Withdrawn has no route and yields the zero value.
func RouteAttrsFor(p *config.PolicyConfig, state model.VIPState) model.RouteAttrs {
	if state != model.StatePessimized {
		return model.RouteAttrs{}
	}
	attrs := model.RouteAttrs{ASPathPrepend: defaultLowerPriorityPrepend}
	if lp := p.LowerPriority; lp != nil {
		if lp.ASPathPrepend != nil {
			attrs.ASPathPrepend = *lp.ASPathPrepend
		}
		attrs.Communities = lp.Communities
	}
	return attrs
}
