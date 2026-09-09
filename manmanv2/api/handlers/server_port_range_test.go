package handlers

import (
	"context"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// mockServerPortRangeRepository records Replace calls so tests can assert
// what the handler sent (FR12, task #2095).
type mockServerPortRangeRepository struct {
	rangesByServer map[int64][]*manman.ServerAllowedPortRange
	nextRangeID    int64
}

func newMockServerPortRangeRepository() *mockServerPortRangeRepository {
	return &mockServerPortRangeRepository{
		rangesByServer: make(map[int64][]*manman.ServerAllowedPortRange),
		nextRangeID:    1,
	}
}

func (m *mockServerPortRangeRepository) List(_ context.Context, serverID int64) ([]*manman.ServerAllowedPortRange, error) {
	out := []*manman.ServerAllowedPortRange{}
	for _, r := range m.rangesByServer[serverID] {
		out = append(out, r)
	}
	return out, nil
}

func (m *mockServerPortRangeRepository) Replace(_ context.Context, serverID int64, ranges []*manman.ServerAllowedPortRange) ([]*manman.ServerAllowedPortRange, error) {
	stored := make([]*manman.ServerAllowedPortRange, 0, len(ranges))
	for _, r := range ranges {
		if r == nil {
			continue
		}
		stored = append(stored, &manman.ServerAllowedPortRange{
			RangeID:   m.nextRangeID,
			ServerID:  serverID,
			StartPort: r.StartPort,
			EndPort:   r.EndPort,
			Protocol:  r.Protocol,
		})
		m.nextRangeID++
	}
	m.rangesByServer[serverID] = stored
	return stored, nil
}
