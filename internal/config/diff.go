package config

import (
	"reflect"
)

// RestartRequired lists the config sections that differ between the running
// config and a candidate and cannot change at runtime. Only vips and
// logging.level reload live; everything else needs a restart, and a reload
// that touches it is rejected as a whole.
func RestartRequired(running, candidate *Config) []string {
	var changed []string
	if running.Schema != candidate.Schema {
		changed = append(changed, "schema")
	}
	if !reflect.DeepEqual(running.Router, candidate.Router) {
		changed = append(changed, "router")
	}
	if !reflect.DeepEqual(running.API, candidate.API) {
		changed = append(changed, "api")
	}
	rl, cl := running.Logging, candidate.Logging
	rl.Level, cl.Level = "", ""
	if !reflect.DeepEqual(rl, cl) {
		changed = append(changed, "logging (other than level)")
	}
	if !reflect.DeepEqual(running.OTel, candidate.OTel) {
		changed = append(changed, "otel")
	}
	if !reflect.DeepEqual(running.Paths, candidate.Paths) {
		changed = append(changed, "paths")
	}
	rb, cb := running.BGP, candidate.BGP
	rb.Neighbors, cb.Neighbors = nil, nil
	if !reflect.DeepEqual(rb, cb) {
		changed = append(changed, "bgp")
	}
	if !reflect.DeepEqual(running.BGP.Neighbors, candidate.BGP.Neighbors) {
		changed = append(changed, "bgp.neighbors")
	}
	return changed
}

// VIPChanges is the result of DiffVIPs.
type VIPChanges struct {
	// Added VIPs appear only in the candidate.
	Added []*VIPConfig
	// Removed VIPs appear only in the running config. A VIP whose prefix
	// changed is listed here and in Added: the old route must go and the
	// new one must prove health.
	Removed []*VIPConfig
	// Changed VIPs exist in both with the same prefix but differ.
	Changed []VIPChange
	// Unchanged names, in candidate order.
	Unchanged []string
}

// VIPChange describes how one VIP differs.
type VIPChange struct {
	Old, New *VIPConfig
	// ChecksChanged lists the checks (by effective name) whose definition
	// differs or that are new in the candidate: every one of them must
	// reach a verdict before the VIP switches to the new definition. When
	// require_local_address or local_interface changed, every check is
	// listed, since the guard wraps each checker.
	ChecksChanged []*CheckConfig
	// ChecksRemoved lists running checks absent from the candidate.
	ChecksRemoved []*CheckConfig
	// PolicyChanged is set when policy or checks_policy differ.
	PolicyChanged bool
}

// IsEmpty reports whether nothing changed.
func (c VIPChanges) IsEmpty() bool {
	return len(c.Added) == 0 && len(c.Removed) == 0 && len(c.Changed) == 0
}

// DiffVIPs compares two VIP lists by name. Both must have defaults applied.
func DiffVIPs(running, candidate []VIPConfig) VIPChanges {
	var out VIPChanges
	old := make(map[string]*VIPConfig, len(running))
	for i := range running {
		old[running[i].Name] = &running[i]
	}
	seen := make(map[string]bool, len(candidate))
	for i := range candidate {
		nv := &candidate[i]
		seen[nv.Name] = true
		ov, ok := old[nv.Name]
		switch {
		case !ok || ov.Prefix != nv.Prefix:
			if ok {
				out.Removed = append(out.Removed, ov)
			}
			out.Added = append(out.Added, nv)
		case reflect.DeepEqual(*ov, *nv):
			out.Unchanged = append(out.Unchanged, nv.Name)
		default:
			out.Changed = append(out.Changed, diffVIP(ov, nv))
		}
	}
	for i := range running {
		if !seen[running[i].Name] {
			out.Removed = append(out.Removed, &running[i])
		}
	}
	return out
}

func diffVIP(ov, nv *VIPConfig) VIPChange {
	ch := VIPChange{Old: ov, New: nv}
	ch.PolicyChanged = !reflect.DeepEqual(ov.Policy, nv.Policy) || ov.EffectiveChecksPolicy() != nv.EffectiveChecksPolicy()
	guardChanged := ov.RequireLocalAddress != nv.RequireLocalAddress || ov.LocalInterface != nv.LocalInterface

	oldChecks := make(map[string]*CheckConfig)
	for _, c := range ov.CheckList() {
		oldChecks[c.EffectiveName()] = c
	}
	newNames := make(map[string]bool)
	for _, c := range nv.CheckList() {
		name := c.EffectiveName()
		newNames[name] = true
		oc, ok := oldChecks[name]
		if !ok || guardChanged || !reflect.DeepEqual(*oc, *c) {
			ch.ChecksChanged = append(ch.ChecksChanged, c)
		}
	}
	for _, c := range ov.CheckList() {
		if !newNames[c.EffectiveName()] {
			ch.ChecksRemoved = append(ch.ChecksRemoved, c)
		}
	}
	return ch
}
