/*
 * Copyright 2026 NVIDIA CORPORATION
 * SPDX-License-Identifier: Apache-2.0
 */

package nautobot

import (
	"context"
	"net/http"
	"slices"
	"sort"

	"github.com/dsx-ai-factory/topograph/internal/httperr"
)

func hasRole(d device, roles []string) bool {
	return d.Role != nil && slices.Contains(roles, d.Role.Name)
}

// discover starts with switches selected by role/location/platform and collects compute
// endpoints from their cable peers. Only connected switches absent from the
// initial result need additional requests. Each switch UUID is fetched once.
func (p *Provider) discover(ctx context.Context, pageSize *int) ([]device, *httperr.Error) {
	batchSize := defaultPageSize
	if pageSize != nil && *pageSize > 0 {
		batchSize = *pageSize
	}
	seeds, httpErr := p.devices(ctx, nil, batchSize)
	if httpErr != nil {
		return nil, httpErr
	}
	devices := []device{}
	scheduled := map[string]bool{}
	frontier := []string{}
	addPeers := func(d device) {
		for _, iface := range d.Interfaces {
			if iface.Peer == nil || iface.Peer.Device == nil {
				continue
			}
			peer := *iface.Peer.Device
			if peer.ID == "" || peer.Name == "" || p.excluded(peer) || !hasRole(peer, p.params.SwitchRoles) || scheduled[peer.ID] {
				continue
			}
			scheduled[peer.ID] = true
			frontier = append(frontier, peer.ID)
		}
	}
	// Register all seeds before following peers, avoiding redundant requests for
	// switches already present in the initial inventory query.
	for _, seed := range seeds {
		if p.excluded(seed) {
			continue
		}
		if seed.ID == "" || seed.Name == "" || !hasRole(seed, p.params.SwitchRoles) || scheduled[seed.ID] {
			return nil, httperr.NewError(http.StatusBadGateway, "Nautobot returned invalid or duplicate switch identity")
		}
		scheduled[seed.ID] = true
		devices = append(devices, seed)
	}
	for _, seed := range devices {
		addPeers(seed)
	}
	for len(frontier) != 0 {
		sort.Strings(frontier)
		batch := slices.Clone(frontier[:min(batchSize, len(frontier))])
		frontier = frontier[len(batch):]
		if err := ctx.Err(); err != nil {
			return nil, httperr.NewError(http.StatusBadGateway, "Nautobot discovery canceled")
		}
		switches, httpErr := p.devices(ctx, batch, batchSize)
		if httpErr != nil {
			return nil, httpErr
		}
		pending := make(map[string]bool, len(batch))
		for _, id := range batch {
			pending[id] = true
		}
		for _, sw := range switches {
			if !pending[sw.ID] || sw.Name == "" || p.excluded(sw) || !hasRole(sw, p.params.SwitchRoles) {
				return nil, httperr.NewError(http.StatusBadGateway, "Nautobot returned an unexpected, duplicate, or invalid switch")
			}
			delete(pending, sw.ID)
			devices = append(devices, sw)
			addPeers(sw)
		}
		if len(pending) != 0 {
			return nil, httperr.NewError(http.StatusBadGateway, "Nautobot omitted connected switches; check inventory consistency and token permissions")
		}
	}
	// A compute endpoint may appear on several ports or switches. Keep one
	// identity record; inventory builds the links from each switch's interfaces.
	hosts := map[string]device{}
	for _, sw := range devices {
		for _, iface := range sw.Interfaces {
			if iface.Peer == nil || iface.Peer.Device == nil {
				continue
			}
			peer := *iface.Peer.Device
			if p.excluded(peer) || !hasRole(peer, p.params.DeviceRole) {
				continue
			}
			if peer.ID == "" || peer.Name == "" || scheduled[peer.ID] {
				return nil, httperr.NewError(http.StatusBadGateway, "Nautobot returned invalid compute peer identity")
			}
			if previous, exists := hosts[peer.ID]; exists {
				if previous.Name != peer.Name || (previous.Serial != "" && peer.Serial != "" && previous.Serial != peer.Serial) {
					return nil, httperr.NewError(http.StatusBadGateway, "Nautobot returned conflicting compute peer identities")
				}
				if peer.Serial == "" {
					peer.Serial = previous.Serial
				}
			}
			hosts[peer.ID] = peer
		}
	}
	hostIDs := make([]string, 0, len(hosts))
	for id := range hosts {
		hostIDs = append(hostIDs, id)
	}
	sort.Strings(hostIDs)
	for _, id := range hostIDs {
		devices = append(devices, hosts[id])
	}
	return devices, nil
}
