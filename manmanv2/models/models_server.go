package manman

import "time"

// Server represents a physical/virtual machine running the host manager
type Server struct {
	ServerID          int64      `db:"server_id"`
	Name              string     `db:"name"`
	Status            string     `db:"status"`
	Environment       *string    `db:"environment"`
	LastSeen          *time.Time `db:"last_seen"`
	IsDefault         bool       `db:"is_default"`
	HostPublicAddress *string    `db:"host_public_address"`
	// DrainState/DrainRequestedAt: host drain state (#2360, manmanv2 M6,
	// C29 groundwork). Inert here -- no cordon enforcement or eviction
	// yet, just the persisted transitions via DrainServer/UndrainServer.
	DrainState       string     `db:"drain_state"`
	DrainRequestedAt *time.Time `db:"drain_requested_at"`
}

// ServerAllowedPortRange is one allowed host-port range for a server
// (FR12, migration 038 / task #2095). Reuses the wire PortRange shape
// (start/end/protocol). Empty set for a server = host-port assignment is
// unconstrained (today's behavior, SB-1.2); enforcement lives in the
// allocation path (dependent task). ServerCapabilities.available_ports is
// dormant and deliberately not built on.
type ServerAllowedPortRange struct {
	RangeID   int64  `db:"range_id"`
	ServerID  int64  `db:"server_id"`
	StartPort int32  `db:"start_port"`
	EndPort   int32  `db:"end_port"`
	Protocol  string `db:"protocol"` // 'TCP' | 'UDP'
}

// ServerCapability represents the resources available on a server
type ServerCapability struct {
	CapabilityID           int64      `db:"capability_id"`
	ServerID               int64      `db:"server_id"`
	TotalMemoryMB          int32      `db:"total_memory_mb"`
	AvailableMemoryMB      int32      `db:"available_memory_mb"`
	CPUCores               int32      `db:"cpu_cores"`
	AvailableCPUMillicores int32      `db:"available_cpu_millicores"`
	DockerVersion          string     `db:"docker_version"`
	RecordedAt             *time.Time `db:"recorded_at"`
}

// ServerPort represents port allocation tracking at server level.
// Session-scoped since migration 014_port_allocation_per_session dropped
// the SGC-scoped sgc_id column (task #2142, plan #2080).
type ServerPort struct {
	ServerID    int64     `db:"server_id"`
	Port        int       `db:"port"`
	Protocol    string    `db:"protocol"`
	SessionID   *int64    `db:"session_id"`
	AllocatedAt time.Time `db:"allocated_at"`
}
