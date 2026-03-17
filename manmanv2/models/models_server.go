package manman

import "time"

// Server represents a physical/virtual machine running the host manager
type Server struct {
	ServerID    int64      `db:"server_id"`
	Name        string     `db:"name"`
	Status      string     `db:"status"`
	Environment *string    `db:"environment"`
	LastSeen    *time.Time `db:"last_seen"`
	IsDefault   bool       `db:"is_default"`
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

// ServerPort represents port allocation tracking at server level
type ServerPort struct {
	ServerID    int64     `db:"server_id"`
	Port        int       `db:"port"`
	Protocol    string    `db:"protocol"`
	SGCID       *int64    `db:"sgc_id"`
	SessionID   *int64    `db:"session_id"`
	AllocatedAt time.Time `db:"allocated_at"`
}
