package gateway

import (
	"context"

	"github.com/kuangjm123/AIGate/internal/routes"
	"github.com/kuangjm123/AIGate/internal/state"
)

// planScope is one group above a seat. Two seats may fall through to each
// other when one of their common scopes allows that failure class.
type planScope struct {
	id     string
	allows func(string) bool
}

type groupPlanMember struct {
	ref     string
	seats   []seatCandidate
	blocked map[string]bool
}

// groupPlan is the ordered, flat result of one group. The flat list is what
// the attempt loop walks; Scopes on each seat preserve the group boundaries
// used to decide whether a failure may move to the next seat.
type groupPlan struct {
	route      routePick
	seats      []seatCandidate
	picked     string
	blocked    map[string]bool
	pool       string
	strategyID string
}

// groupPool is the strategy memory key of one group. Explicit groups use a
// "group/" prefix so a group id and a route id do not share rotation or weight
// state. An auto group keeps the old bare-model pool.
func groupPool(id, asked string) string {
	if id != "" {
		return "group/" + id
	}
	return poolOf("", asked)
}

// planGroup orders every member, then orders the member heads with the group's
// strategy. A member's own seats are ordered first, so an inner group finishes
// its own order before the outer group moves to another member.
func (s *Server) planGroup(ctx context.Context, cfg state.Config, asked string, g state.Group, inherited string, stack map[string]bool) *groupPlan {
	return s.planGroupWithExtra(ctx, cfg, asked, g, inherited, stack, nil)
}

// planGroupWithExtra is planGroup with seats that are not provider/model group
// members. A bare model's readable subscription accounts join the implicit
// group this way, so they are ordered with the member heads instead of being
// left out when two providers make the auto group.
func (s *Server) planGroupWithExtra(ctx context.Context, cfg state.Config, asked string, g state.Group, inherited string, stack map[string]bool, extra []seatCandidate) *groupPlan {
	pool := groupPool(g.ID, asked)
	strategy := groupStrategy(g.Routing, inherited)
	scope := &planScope{id: g.ID, allows: func(class string) bool { return g.Allows(state.ErrorClass(class)) }}
	if stack == nil {
		stack = map[string]bool{}
	}
	if g.ID != "" {
		if stack[g.ID] {
			return &groupPlan{
				route: routePick{id: g.ID, found: true, allows: scope.allows},
				pool:  pool, strategyID: strategy,
			}
		}
		stack[g.ID] = true
		defer delete(stack, g.ID)
	}

	members := make([]groupPlanMember, 0, len(g.Members))
	for _, ref := range g.Members {
		member, ok := cfg.ResolveGroupMember(ref)
		if !ok {
			continue
		}
		var seats []seatCandidate
		var blocked map[string]bool
		if member.Nested {
			child, found := cfg.GroupByID(member.GroupID)
			if !found || stack[child.ID] {
				continue
			}
			childPlan := s.planGroup(ctx, cfg, asked, child, strategy, stack)
			seats, blocked = childPlan.seats, childPlan.blocked
		} else {
			seats = providerModelCandidates(cfg, member.ProviderID, member.Model)
			leaf := groupLeafStrategy(strategy)
			leafPool := pool + "/member/" + member.Ref
			ordered, _, leafBlocked, _, _ := s.orderSeats(ctx, state.Route{ID: leafPool, Routing: leaf}, member.Model, seats)
			seats, blocked = ordered, leafBlocked
		}
		if len(seats) == 0 {
			continue
		}
		for i := range seats {
			if member.Fixed != "" {
				seats[i].ReasoningFixed = member.Fixed
			}
			if len(member.Allowed) > 0 {
				seats[i].ReasoningAllowed = append([]string(nil), member.Allowed...)
			}
			seats[i].TopMember = member.Ref
			seats[i].Weight = 0
		}
		members = append(members, groupPlanMember{ref: member.Ref, seats: seats, blocked: blocked})
	}
	for _, seat := range extra {
		if seat.Key == "" {
			continue
		}
		// A synthetic ref keeps an account head from colliding with a
		// provider/model member spelled the same way.
		seat.TopMember = "seat/" + seat.Key
		seat.Weight = 0
		members = append(members, groupPlanMember{ref: seat.TopMember, seats: []seatCandidate{seat}})
	}

	out := &groupPlan{
		route: routePick{id: g.ID, found: g.ID != "" || len(members) > 0, allows: scope.allows},
		pool:  pool, strategyID: strategy,
	}
	if len(members) == 0 {
		return out
	}

	heads := make([]seatCandidate, len(members))
	for i, member := range members {
		head := member.seats[0]
		head.Weight = g.Weights[member.ref]
		heads[i] = head
	}
	orderedHeads, picked, outerBlocked, _, _ := s.orderSeats(ctx, state.Route{ID: pool, Routing: strategy}, asked, heads)
	byRef := make(map[string]groupPlanMember, len(members))
	for _, member := range members {
		byRef[member.ref] = member
	}
	ordered := make([]groupPlanMember, 0, len(members))
	seen := make(map[string]bool, len(members))
	for _, head := range orderedHeads {
		member, ok := byRef[head.TopMember]
		if !ok || seen[head.TopMember] {
			continue
		}
		seen[head.TopMember] = true
		ordered = append(ordered, member)
	}
	for _, member := range members {
		if !seen[member.ref] {
			ordered = append(ordered, member)
		}
	}

	blocked := map[string]bool{}
	for _, member := range ordered {
		for key := range member.blocked {
			blocked[key] = true
		}
		for i := range member.seats {
			seat := member.seats[i]
			if len(g.Levels) > 0 && len(seat.ReasoningAllowed) == 0 {
				seat.ReasoningAllowed = append([]string(nil), g.Levels...)
			}
			if len(g.ReasoningMap) > 0 && seat.ReasoningMap == nil {
				seat.ReasoningMap = g.ReasoningMap
				seat.ReasoningMember = seat.TopMember
			}
			seat.Scopes = append([]*planScope{scope}, seat.Scopes...)
			out.seats = append(out.seats, seat)
		}
	}
	for key := range outerBlocked {
		blocked[key] = true
	}
	if len(blocked) == 0 {
		blocked = nil
	}
	out.blocked = blocked
	out.picked = picked
	if out.picked == "" && len(out.seats) > 0 {
		out.picked = out.seats[0].Key
	}
	return out
}

// groupStrategy resolves a group's strategy. An empty routing inherits an
// outer strategy only when the outer strategy is inheritable. Order and
// weight leave the inner group on Smart.
func groupStrategy(own, inherited string) string {
	if own != "" {
		return own
	}
	if inheritableStrategy(inherited) {
		return inherited
	}
	return routes.Smart
}

func inheritableStrategy(id string) bool {
	switch id {
	case routes.Smart, routes.Rotate, routes.Usage, routes.Pace, routes.Free, routes.Fast, routes.Drain:
		return true
	}
	return false
}

// groupLeafStrategy orders a provider's keys inside a group member. Weight is
// keyed by member refs, not keys, so a weighted outer group uses Smart for the
// keys of one member.
func groupLeafStrategy(id string) string {
	if id == routes.Weight {
		return routes.Smart
	}
	return id
}

// scopesAllow reports whether a common group scope permits moving from a to b.
func scopesAllow(a, b seatCandidate, class string) bool {
	for _, left := range a.Scopes {
		for _, right := range b.Scopes {
			if left == right && left.allows != nil && left.allows(class) {
				return true
			}
		}
	}
	return false
}

// autoGroupMembers builds the implicit group for a bare model listed by more
// than one provider. The provider order is the config order.
func autoGroupMembers(providerIDs []string, model string) []string {
	out := make([]string, 0, len(providerIDs))
	for _, id := range providerIDs {
		out = append(out, id+"/"+model)
	}
	return out
}

// retainSeatOrder keeps the planned order while dropping seats a media check
// removed.
func retainSeatOrder(order, keep []seatCandidate) []seatCandidate {
	allowed := make(map[string]bool, len(keep))
	for _, seat := range keep {
		allowed[seat.Key] = true
	}
	out := make([]seatCandidate, 0, len(keep))
	for _, seat := range order {
		if allowed[seat.Key] {
			out = append(out, seat)
		}
	}
	return out
}

func seatPresent(seats []seatCandidate, key string) bool {
	if key == "" {
		return false
	}
	for _, seat := range seats {
		if seat.Key == key {
			return true
		}
	}
	return false
}

func firstSeatKey(seats []seatCandidate, blocked map[string]bool) string {
	for _, seat := range seats {
		if !blocked[seat.Key] {
			return seat.Key
		}
	}
	if len(seats) > 0 {
		return seats[0].Key
	}
	return ""
}

func seatByKey(seats []seatCandidate, key string) seatCandidate {
	for _, seat := range seats {
		if seat.Key == key {
			return seat
		}
	}
	return seatCandidate{}
}
