package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	api "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppacket "github.com/osrg/gobgp/v4/pkg/packet/bgp"
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
		req := apiutil.ListPathRequest{
			TableType: api.TableType_TABLE_TYPE_ADJ_OUT,
			Name:      peer.Address,
			Family:    bgppacket.RF_IPv4_UC,
			Prefixes: []*apiutil.LookupPrefix{{
				Prefix:       prefix,
				LookupOption: apiutil.LOOKUP_EXACT,
			}},
		}

		err := m.server.ListPath(req, func(nlri bgppacket.NLRI, paths []*apiutil.Path) {
			for _, p := range paths {
				labels := routeStateLabelsFromPath(nlri, p, peer.Address, peerASN)
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

func routeStateLabelsFromPath(nlri bgppacket.NLRI, path *apiutil.Path, peerIP, peerASN string) routeStateLabels {
	labels := routeStateLabels{
		nlri:    nlri.String(),
		peerIP:  peerIP,
		peerASN: peerASN,
		family:  familyName(path.Family),
	}
	if labels.family == "" {
		labels.family = "ipv4-unicast"
	}
	if labels.nlri == "" {
		labels.nlri = decodeNLRIPrefix(path)
	}

	for _, attr := range path.Attrs {
		switch v := attr.(type) {
		case *bgppacket.PathAttributeAsPath:
			labels.asPath = stringifyASPath(v)
		case *bgppacket.PathAttributeCommunities:
			labels.communities = stringifyCommunities(v.Value)
		case *bgppacket.PathAttributeLocalPref:
			labels.localPreference = strconv.FormatUint(uint64(v.Value), 10)
		case *bgppacket.PathAttributeMultiExitDisc:
			labels.med = strconv.FormatUint(uint64(v.Value), 10)
		}
	}

	return labels
}

func decodeNLRIPrefix(path *apiutil.Path) string {
	if path == nil || path.Nlri == nil {
		return ""
	}
	return path.Nlri.String()
}

func stringifyASPath(attr *bgppacket.PathAttributeAsPath) string {
	if attr == nil {
		return ""
	}
	return bgppacket.AsPathString(attr)
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

func familyName(f bgppacket.Family) string {
	switch f {
	case bgppacket.RF_IPv4_UC:
		return "ipv4-unicast"
	case bgppacket.RF_IPv6_UC:
		return "ipv6-unicast"
	default:
		return strings.ToLower(f.String())
	}
}
