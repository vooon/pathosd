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

// BaseRouteAttrs returns the attributes from the announce block: the base
// prepend and communities carried by every announcement of the VIP.
func BaseRouteAttrs(p *config.PolicyConfig) model.RouteAttrs {
	var attrs model.RouteAttrs
	if a := p.Announce; a != nil {
		if a.ASPathPrepend != nil {
			attrs.ASPathPrepend = *a.ASPathPrepend
		}
		attrs.Communities = append([]string(nil), a.Communities...)
	}
	return attrs
}

// RouteAttrsFor returns the BGP attributes to originate the VIP with in the
// given state:
//
//   - announced:  the announce block (base prepend + communities)
//   - pessimized: base prepend + lower_priority prepend, and the union of
//     both community lists, so a degraded node never outranks a healthy
//     node with a larger base prepend
//   - withdrawn:  zero value (no route)
func RouteAttrsFor(p *config.PolicyConfig, state model.VIPState) model.RouteAttrs {
	switch state {
	case model.StateAnnounced:
		return BaseRouteAttrs(p)
	case model.StatePessimized:
		attrs := BaseRouteAttrs(p)
		lowerPrepend := defaultLowerPriorityPrepend
		var lowerCommunities []string
		if lp := p.LowerPriority; lp != nil {
			if lp.ASPathPrepend != nil {
				lowerPrepend = *lp.ASPathPrepend
			}
			lowerCommunities = lp.Communities
		}
		attrs.ASPathPrepend += lowerPrepend
		attrs.Communities = mergeCommunities(attrs.Communities, lowerCommunities)
		return attrs
	default:
		return model.RouteAttrs{}
	}
}

// mergeCommunities appends extra to base, dropping duplicates and keeping
// first-seen order. nil is returned when the result is empty.
func mergeCommunities(base, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, c := range list {
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}
