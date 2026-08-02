package routing

import (
	"errors"
	"strconv"
	"strings"

	dbmodel "github.com/songquanpeng/one-api/model"
)

// ErrChannelNotEligible is returned when a caller asks to pin a session to a
// channel that cannot serve the requested (group, model) pair.
var ErrChannelNotEligible = errors.New("channel cannot serve this group/model")

// NodeInfo describes one upstream node (channel) that can serve a
// (group, model) pair, along with its live sticky-routing state. It is what a
// client needs in order to offer a "switch provider" picker.
type NodeInfo struct {
	ChannelId int    `json:"channel_id"`
	Name      string `json:"name"`
	// UpstreamModel is what this node actually receives after the channel's
	// model mapping is applied, e.g. coding_medium -> MiniMax-M3. This is the
	// part a user actually cares about when switching provider.
	UpstreamModel string `json:"upstream_model"`
	Priority      int64  `json:"priority"`
	// Current reports whether the session is pinned to this node right now.
	Current bool `json:"current"`
	// CoolingDown reports whether the node is temporarily excluded after a
	// recent failure.
	CoolingDown bool `json:"cooling_down"`
	Sessions    int  `json:"sessions"`
}

// Nodes lists every channel able to serve (group, model), annotated with the
// live state of the given session. session may be empty, in which case no node
// is marked Current.
func (r *Router) Nodes(group, model, session string) []NodeInfo {
	candidates := r.provider.SatisfiedChannels(group, model)
	if len(candidates) == 0 {
		return nil
	}
	pinned := -1
	if session != "" {
		if id, ok := r.store.Get(r.key(group, model, session)); ok {
			pinned = id
		}
	}
	counts := make(map[int]int)
	cooling := make(map[int]bool)
	for _, st := range r.store.ChannelStates() {
		counts[st.ChannelId] = st.Sessions
		cooling[st.ChannelId] = !st.CoolingUntil.IsZero()
	}
	nodes := make([]NodeInfo, 0, len(candidates))
	for _, ch := range candidates {
		nodes = append(nodes, NodeInfo{
			ChannelId:     ch.Id,
			Name:          ch.Name,
			UpstreamModel: upstreamModel(ch, model),
			Priority:      ch.GetPriority(),
			Current:       ch.Id == pinned,
			CoolingDown:   cooling[ch.Id],
			Sessions:      counts[ch.Id],
		})
	}
	return nodes
}

// upstreamModel resolves what the channel forwards upstream for model, applying
// the channel's model mapping. It mirrors how the relay rewrites the model name,
// including openrouter-style "~" prefixes being stripped.
func upstreamModel(ch *dbmodel.Channel, model string) string {
	mapping := ch.GetModelMapping()
	if mapped, ok := mapping[model]; ok && mapped != "" {
		return strings.TrimPrefix(mapped, "~")
	}
	return model
}

// Pin binds a session to a specific channel, overriding the automatic choice.
// The channel must currently be able to serve (group, model), so a client
// cannot pin a session onto a disabled or unrelated node.
//
// Pinning also clears any cooldown recorded for that channel: the user is
// explicitly asking for this node, which overrides an earlier automatic
// judgement that it was unhealthy.
func (r *Router) Pin(group, model, session string, channelId int) (*dbmodel.Channel, error) {
	if session == "" {
		return nil, errors.New("no session to pin")
	}
	ch := findChannel(r.provider.SatisfiedChannels(group, model), channelId)
	if ch == nil {
		return nil, ErrChannelNotEligible
	}
	r.store.ClearCooldown(channelId)
	r.store.Rebind(r.key(group, model, session), session, group, model, channelId)
	return ch, nil
}

// Next rotates a session to the following eligible node in channel-id order,
// wrapping around. It powers a "cycle provider" command where the user does not
// care which node comes next, only that it is a different one.
//
// Cooled-down nodes are skipped unless every alternative is cooling down, in
// which case the rotation still moves (the user explicitly asked to switch).
func (r *Router) Next(group, model, session string) (*dbmodel.Channel, error) {
	if session == "" {
		return nil, errors.New("no session to rotate")
	}
	candidates := r.provider.SatisfiedChannels(group, model)
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}
	ordered := make([]*dbmodel.Channel, len(candidates))
	copy(ordered, candidates)
	sortByChannelId(ordered)

	current, _ := r.store.Get(r.key(group, model, session))
	startIdx := 0
	for i, ch := range ordered {
		if ch.Id == current {
			startIdx = i + 1
			break
		}
	}
	// Two passes: prefer a healthy node, then accept a cooling one.
	for _, allowCooling := range []bool{false, true} {
		for offset := 0; offset < len(ordered); offset++ {
			ch := ordered[(startIdx+offset)%len(ordered)]
			if ch.Id == current && len(ordered) > 1 {
				continue
			}
			if !allowCooling && r.store.IsCooledDownNow(ch.Id) {
				continue
			}
			return r.Pin(group, model, session, ch.Id)
		}
	}
	return nil, ErrNoChannel
}

// Unpin drops the session's binding so the next request is routed
// automatically again.
func (r *Router) Unpin(group, model, session string) bool {
	if session == "" {
		return false
	}
	return r.store.ForgetSession(session) > 0
}

func sortByChannelId(channels []*dbmodel.Channel) {
	for i := 1; i < len(channels); i++ {
		for j := i; j > 0 && channels[j].Id < channels[j-1].Id; j-- {
			channels[j], channels[j-1] = channels[j-1], channels[j]
		}
	}
}

// ParseChannelId accepts either a numeric channel id or a channel name and
// resolves it against the nodes eligible for (group, model).
func (r *Router) ParseChannelId(group, model, target string) (int, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, errors.New("empty channel target")
	}
	candidates := r.provider.SatisfiedChannels(group, model)
	if id, err := strconv.Atoi(target); err == nil {
		if findChannel(candidates, id) == nil {
			return 0, ErrChannelNotEligible
		}
		return id, nil
	}
	// Exact name match first, then case-insensitive.
	for _, ch := range candidates {
		if ch.Name == target {
			return ch.Id, nil
		}
	}
	lowered := strings.ToLower(target)
	var matched *dbmodel.Channel
	for _, ch := range candidates {
		if strings.ToLower(ch.Name) == lowered {
			if matched != nil {
				return 0, errors.New("ambiguous channel name: " + target)
			}
			matched = ch
		}
	}
	if matched == nil {
		return 0, ErrChannelNotEligible
	}
	return matched.Id, nil
}
