package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/profilevalidation"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

// inboundHasEnabledRuntimeProfiles keeps its historical name for the restart
// call sites. It now reports active runtime-marked structured profiles because
// their listener topology is automatic; markerless subscription-only entries
// and disabled drafts remain outside the runtime compiler.
func inboundHasEnabledRuntimeProfiles(streamSettings string) bool {
	var stream map[string]any
	if json.Unmarshal([]byte(streamSettings), &stream) != nil || stream == nil {
		return false
	}
	entries, _ := stream["externalProxy"].([]any)
	for _, raw := range entries {
		profile, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if automaticRuntimeProfileActive(profile) {
			return true
		}
	}
	return false
}

// normalizeAutomaticRuntimeProfilesForSave persists stable hidden IDs and
// removes the obsolete user-controlled topology fields. It is idempotent and
// leaves legacy subscription-only profiles plus disabled drafts byte-equivalent
// after JSON decoding.
func normalizeAutomaticRuntimeProfilesForSave(inbound *model.Inbound) (bool, error) {
	if inbound == nil || strings.TrimSpace(inbound.StreamSettings) == "" {
		return false, nil
	}
	var stream map[string]any
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		return false, fmt.Errorf("invalid stream settings: %w", err)
	}
	changed, err := normalizeAutomaticRuntimeProfilesInPlace(stream)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	encoded, err := json.Marshal(stream)
	if err != nil {
		return false, fmt.Errorf("marshal automatic runtime metadata: %w", err)
	}
	inbound.StreamSettings = string(encoded)
	return true, nil
}

// runtimeBindingsForInbound resolves the operator-visible socket plan for one
// logical inbound. Same-socket TCP profile listeners are collapsed into one
// frontmux public binding by the topology compiler; distinct TCP and UDP
// sockets remain separate. Reusing the compiler keeps save-time validation and
// generated runtime behavior identical.
func runtimeBindingsForInbound(inbound *model.Inbound) ([]runtimeSocketBinding, error) {
	if inbound == nil {
		return nil, nil
	}

	rawStream := map[string]any{}
	if inbound.StreamSettings != "" {
		if err := json.Unmarshal([]byte(inbound.StreamSettings), &rawStream); err != nil {
			return nil, fmt.Errorf("invalid stream settings: %w", err)
		}
	}
	topology, err := compileRuntimeProfileTopology(
		inbound,
		rawStream,
		newSharedBackendAllocator(nil),
	)
	if err != nil {
		return nil, err
	}
	bindings := append([]runtimeSocketBinding(nil), topology.PublicBindings...)

	// Some protocols (notably Shadowsocks and Mixed) can own both TCP and UDP
	// on one numeric port. The stream topology represents its active stream
	// family; preserve any additional parent family here for global collision
	// validation. Runtime profiles themselves are currently supported only by
	// single-family VLESS/VMESS/Trojan/Shadowsocks stream projections.
	transports := inboundTransports(inbound.Protocol, inbound.StreamSettings, inbound.Settings)
	listen, listenErr := normalizeRuntimeListen(inbound.Listen, false)
	if listenErr != nil {
		return nil, listenErr
	}
	for _, transport := range []struct {
		bit  transportBits
		name string
	}{
		{transportTCP, "tcp"},
		{transportUDP, "udp"},
	} {
		if transports&transport.bit == 0 {
			continue
		}
		candidate := runtimeSocketBinding{
			Tag:       inbound.Tag,
			Listen:    listen,
			Port:      inbound.Port,
			Transport: transport.name,
		}
		found := false
		for _, existing := range bindings {
			if runtimeBindingGroupKey(existing) == runtimeBindingGroupKey(candidate) {
				found = true
				break
			}
		}
		if !found {
			bindings = append(bindings, candidate)
		}
	}
	return bindings, nil
}

// validateRuntimeProfilesForSave rejects an invalid profile before the DB row
// is committed. It checks the complete same-node socket plan, including parent
// listeners, synthetic profile listeners, and the local Xray API listener.
//
// Remote runtime profiles are accepted only after a successful heartbeat has
// proved that the selected node runs the same logical-inbound compiler. Socket
// conflicts remain scoped by NodeID; certificate paths are checked by the node
// that owns the filesystem when the desired row is reconciled there.
func (s *InboundService) validateRuntimeProfilesForSave(candidate *model.Inbound, ignoreID int) error {
	if candidate == nil {
		return nil
	}

	if err := profilevalidation.ValidateStreamSettings(candidate.StreamSettings, profilevalidation.Options{
		Protocol:              string(candidate.Protocol),
		CheckCertificateFiles: candidate.NodeID == nil,
	}); err != nil {
		return fmt.Errorf("subscription profile semantic validation failed: %w", err)
	}

	hasRuntimeProfiles := inboundHasEnabledRuntimeProfiles(candidate.StreamSettings)
	if strings.HasPrefix(strings.TrimSpace(candidate.Tag), runtimeProfileTagPrefix) ||
		strings.HasPrefix(strings.TrimSpace(candidate.Tag), sharedPortTagPrefix) {
		return fmt.Errorf(
			"inbound tag prefixes %q and %q are reserved for compiled runtime listeners",
			runtimeProfileTagPrefix,
			sharedPortTagPrefix,
		)
	}
	if hasRuntimeProfiles && candidate.NodeID != nil {
		var node model.Node
		if err := database.GetDB().Model(model.Node{}).
			Where("id = ?", *candidate.NodeID).
			First(&node).Error; err != nil {
			return fmt.Errorf("load remote node capability: %w", err)
		}
		if !wirecodec.HasCapability(node.Capabilities, wirecodec.CapRuntimeProfilesV1) {
			return fmt.Errorf(
				"remote node %q does not advertise the %q capability; update the node panel and wait for a successful heartbeat",
				node.Name,
				wirecodec.CapRuntimeProfilesV1,
			)
		}
	}

	candidateBindings, err := runtimeBindingsForInbound(candidate)
	if err != nil {
		return fmt.Errorf("runtime profile validation failed: %w", err)
	}

	existingBindings := make([]runtimeSocketBinding, 0)
	var existing []*model.Inbound
	query := database.GetDB().Model(model.Inbound{})
	if ignoreID > 0 {
		query = query.Where("id != ?", ignoreID)
	}
	if err := query.Find(&existing).Error; err != nil {
		return err
	}

	for _, inbound := range existing {
		if !sameNode(inbound.NodeID, candidate.NodeID) {
			continue
		}
		if hasRuntimeProfiles && (strings.HasPrefix(strings.TrimSpace(inbound.Tag), runtimeProfileTagPrefix) ||
			strings.HasPrefix(strings.TrimSpace(inbound.Tag), sharedPortTagPrefix)) {
			return fmt.Errorf("existing inbound %d uses a reserved runtime-listener tag prefix", inbound.Id)
		}
		bindings, bindErr := runtimeBindingsForInbound(inbound)
		if bindErr != nil {
			// A pre-existing malformed row must not be silently treated as a
			// free socket: surface its identity so the operator can repair it.
			return fmt.Errorf("existing inbound %d runtime profile validation failed: %w", inbound.Id, bindErr)
		}
		existingBindings = append(existingBindings, bindings...)
	}

	if candidate.NodeID == nil {
		existingBindings = append(existingBindings, runtimeSocketBinding{
			Tag:       "api",
			Listen:    "127.0.0.1",
			Port:      reservedAPIPort(),
			Transport: "tcp",
		})
	}

	// Compare the complete candidate plan. This is required even when the
	// candidate has no synthetic profiles: a normal parent listener must not
	// steal a socket already owned by another logical inbound's profile.
	for _, binding := range candidateBindings {
		for _, existing := range existingBindings {
			if !runtimeBindingsConflict(existing, binding) {
				continue
			}
			return fmt.Errorf(
				"runtime socket %s conflicts with inbound %q",
				runtimeBindingLabel(binding), existing.Tag,
			)
		}
	}

	// Also guard candidate-internal collisions. The topology compiler has already
	// collapsed compatible same-TCP-socket routes into one public binding, so any
	// remaining overlap is a real invariant violation.
	for left := 0; left < len(candidateBindings); left++ {
		for right := left + 1; right < len(candidateBindings); right++ {
			if runtimeBindingsConflict(candidateBindings[left], candidateBindings[right]) {
				return fmt.Errorf(
					"runtime socket %s conflicts with inbound %q",
					runtimeBindingLabel(candidateBindings[right]), candidateBindings[left].Tag,
				)
			}
		}
	}
	return nil
}
