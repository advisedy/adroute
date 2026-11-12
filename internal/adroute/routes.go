package adroute

import "strings"

// DefaultRouteID follows the web default model when the client omits "model".
const DefaultRouteID = "adroute/default"

const publicRoutePrefix = "adroute/"

// aggregateNamespace is the reserved first segment of every aggregate route.
const aggregateNamespace = "agg"

// PublicModelID is the only public model identifier: adroute/<group>/<name>.
// Nothing else (UUID, upstream model id, alias) is ever exposed to clients.
func PublicModelID(group, name string) string {
	return publicRoutePrefix + group + "/" + name
}

// ParsePublicModelID splits a public route id into its group name and model
// name. Both parts are validated segment shapes, so a raw upstream model id can
// never be mistaken for a route.
func ParsePublicModelID(id string) (group, name string, ok bool) {
	if !strings.HasPrefix(id, publicRoutePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, publicRoutePrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	if !validGroupName(parts[0]) || !validModelName(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// groupOrder returns groups in their stored creation order, which keeps catalog
// priority deterministic across restarts.
func groupOrder(st State) []Group {
	return append([]Group(nil), st.Groups...)
}

// AggregateRouteID is the only public identifier of a failover chain.
func AggregateRouteID(name string) string {
	return publicRoutePrefix + aggregateNamespace + "/" + name
}

func findAggregate(st State, id string) *Aggregate {
	for i := range st.Aggregates {
		if st.Aggregates[i].ID == id {
			return &st.Aggregates[i]
		}
	}
	return nil
}

func findAggregateByName(st State, name string) *Aggregate {
	for i := range st.Aggregates {
		if st.Aggregates[i].Name == name {
			return &st.Aggregates[i]
		}
	}
	return nil
}

// aggregateMembers expands the chain into member routes, skipping members that
// no longer resolve or are closed. It never fails.
func aggregateMembers(st State, agg *Aggregate) []Route {
	out := []Route{}
	for _, member := range agg.Members {
		for _, m := range st.Models {
			if m.ID == member && !m.Disabled {
				out = append(out, routeFor(st, m))
				break
			}
		}
	}
	return out
}
