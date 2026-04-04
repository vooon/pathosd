package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	api "github.com/osrg/gobgp/v3/api"
)

type routeStateLabels struct {
	nlri            string
	peerIP          string
	peerASN         string
	asPath          string
	communities     string
	localPreference string
	med             string
	family          string
}

func (l routeStateLabels) values() []string {
	return []string{
		l.nlri,
		l.peerIP,
		l.peerASN,
		l.asPath,
		l.communities,
		l.localPreference,
		l.med,
		l.family,
	}
}

func (l routeStateLabels) key() string {
	return strings.Join(l.values(), "\x00")
}

func (m *Manager) syncRouteStateMetric(ctx context.Context, prefix string) {
	m.mu.Lock()
	pathosMetrics := m.metrics
	m.mu.Unlock()
	if pathosMetrics == nil || pathosMetrics.RouteState == nil {
		return
	}
	metric := pathosMetrics.RouteState

	current := make(map[string]routeStateLabels)
	for _, peer := range m.cfg.BGP.Neighbors {
		peerASN := strconv.FormatUint(uint64(peer.PeerASN), 10)
		req := &api.ListPathRequest{
			TableType: api.TableType_ADJ_OUT,
			Name:      peer.Address,
			Family:    &api.Family{Afi: api.Family_AFI_IP, Safi: api.Family_SAFI_UNICAST},
			Prefixes: []*api.TableLookupPrefix{{
				Prefix: prefix,
				Type:   api.TableLookupPrefix_EXACT,
			}},
		}

		err := m.server.ListPath(ctx, req, func(dst *api.Destination) {
			for _, p := range dst.GetPaths() {
				labels := routeStateLabelsFromPath(dst, p, peer.Address, peerASN)
				current[labels.key()] = labels
			}
		})
		if err != nil {
			slog.Warn(
				"failed to refresh route_state metric from adj-rib-out",
				"prefix", prefix,
				"peer", peer.Address,
				"error", err,
			)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	previous := m.routeStateByPrefix[prefix]
	if previous == nil {
		previous = make(map[string]routeStateLabels)
	}

	for key, labels := range previous {
		if _, ok := current[key]; ok {
			continue
		}
		metric.WithLabelValues(labels.values()...).Set(0)
	}
	for _, labels := range current {
		metric.WithLabelValues(labels.values()...).Set(1)
	}

	m.routeStateByPrefix[prefix] = current
}

func routeStateLabelsFromPath(dst *api.Destination, path *api.Path, peerIP, peerASN string) routeStateLabels {
	labels := routeStateLabels{
		nlri:    dst.GetPrefix(),
		peerIP:  peerIP,
		peerASN: peerASN,
		family:  familyName(path.GetFamily()),
	}
	if labels.family == "" {
		labels.family = "ipv4-unicast"
	}
	if labels.nlri == "" {
		labels.nlri = decodeNLRIPrefix(path)
	}

	for _, attr := range path.GetPattrs() {
		switch {
		case attr.MessageIs(&api.AsPathAttribute{}):
			decoded := &api.AsPathAttribute{}
			if err := attr.UnmarshalTo(decoded); err != nil {
				continue
			}
			labels.asPath = stringifyASPath(decoded)
		case attr.MessageIs(&api.CommunitiesAttribute{}):
			decoded := &api.CommunitiesAttribute{}
			if err := attr.UnmarshalTo(decoded); err != nil {
				continue
			}
			labels.communities = stringifyCommunities(decoded.Communities)
		case attr.MessageIs(&api.LocalPrefAttribute{}):
			decoded := &api.LocalPrefAttribute{}
			if err := attr.UnmarshalTo(decoded); err != nil {
				continue
			}
			labels.localPreference = strconv.FormatUint(uint64(decoded.LocalPref), 10)
		case attr.MessageIs(&api.MultiExitDiscAttribute{}):
			decoded := &api.MultiExitDiscAttribute{}
			if err := attr.UnmarshalTo(decoded); err != nil {
				continue
			}
			labels.med = strconv.FormatUint(uint64(decoded.Med), 10)
		}
	}

	return labels
}

func decodeNLRIPrefix(path *api.Path) string {
	nlri := path.GetNlri()
	if nlri == nil || !nlri.MessageIs(&api.IPAddressPrefix{}) {
		return ""
	}
	v := &api.IPAddressPrefix{}
	if err := nlri.UnmarshalTo(v); err != nil {
		return ""
	}
	return fmt.Sprintf("%s/%d", v.Prefix, v.PrefixLen)
}

func stringifyASPath(attr *api.AsPathAttribute) string {
	var values []string
	for _, seg := range attr.GetSegments() {
		for _, asn := range seg.GetNumbers() {
			values = append(values, strconv.FormatUint(uint64(asn), 10))
		}
	}
	return strings.Join(values, " ")
}

func stringifyCommunities(communities []uint32) string {
	if len(communities) == 0 {
		return ""
	}
	out := make([]string, 0, len(communities))
	for _, c := range communities {
		high := c >> 16
		low := c & 0xFFFF
		out = append(out, fmt.Sprintf("%d:%d", high, low))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func familyName(f *api.Family) string {
	if f == nil {
		return ""
	}
	switch {
	case f.Afi == api.Family_AFI_IP && f.Safi == api.Family_SAFI_UNICAST:
		return "ipv4-unicast"
	case f.Afi == api.Family_AFI_IP6 && f.Safi == api.Family_SAFI_UNICAST:
		return "ipv6-unicast"
	default:
		return strings.ToLower(f.String())
	}
}
