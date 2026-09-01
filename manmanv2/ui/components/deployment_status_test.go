package components

import (
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// TestComputeDeploymentStatus_StatusMapping covers test 1 and test 2 of the
// task's Testing section: the two-state gamer-facing rollup (FR2), including
// the fail-closed behaviour for unrecognised/empty statuses and the FR12
// nil-latest-session case.
func TestComputeDeploymentStatus_StatusMapping(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   DeploymentStatus
	}{
		{"running maps to running", "running", DeploymentRunning},
		{"pending maps to stopped", "pending", DeploymentStopped},
		{"starting maps to stopped", "starting", DeploymentStopped},
		{"stopping maps to stopped", "stopping", DeploymentStopped},
		{"stopped maps to stopped", "stopped", DeploymentStopped},
		{"crashed maps to stopped", "crashed", DeploymentStopped},
		{"completed maps to stopped", "completed", DeploymentStopped},
		{"empty status maps to stopped", "", DeploymentStopped},
		{"unknown status maps to stopped (fail closed)", "some-unrecognised-status", DeploymentStopped},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &manmanpb.Session{SessionId: 1, Status: tc.status}
			got := ComputeDeploymentStatus(session)
			if got != tc.want {
				t.Errorf("ComputeDeploymentStatus(status=%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestComputeDeploymentStatus_NilLatestSession is FR12: a deployment that
// never had a session must roll up to stopped.
func TestComputeDeploymentStatus_NilLatestSession(t *testing.T) {
	got := ComputeDeploymentStatus(nil)
	if got != DeploymentStopped {
		t.Errorf("ComputeDeploymentStatus(nil) = %q, want %q", got, DeploymentStopped)
	}
}

// TestLatestSession_NilAndEmpty covers the nil/empty-slice cases of test 3.
func TestLatestSession_NilAndEmpty(t *testing.T) {
	if got := LatestSession(nil); got != nil {
		t.Errorf("LatestSession(nil) = %v, want nil", got)
	}
	if got := LatestSession([]*manmanpb.Session{}); got != nil {
		t.Errorf("LatestSession([]) = %v, want nil", got)
	}
}

// TestLatestSession_NewestByStartedAt covers the mixed-started_at case of
// test 3: the newest session by started_at wins regardless of input order.
func TestLatestSession_NewestByStartedAt(t *testing.T) {
	s1 := &manmanpb.Session{SessionId: 1, StartedAt: 100}
	s2 := &manmanpb.Session{SessionId: 2, StartedAt: 300}
	s3 := &manmanpb.Session{SessionId: 3, StartedAt: 200}

	sessions := []*manmanpb.Session{s1, s2, s3}
	got := LatestSession(sessions)
	if got == nil || got.GetSessionId() != 2 {
		t.Errorf("LatestSession(mixed started_at) = %v, want session_id=2", got)
	}
}

// TestLatestSession_TiedOrZeroStartedAtFallsBackToSessionID covers the
// started_at==0-or-tied fallback of test 3: highest session_id wins.
func TestLatestSession_TiedOrZeroStartedAtFallsBackToSessionID(t *testing.T) {
	t.Run("all zero started_at", func(t *testing.T) {
		s1 := &manmanpb.Session{SessionId: 5, StartedAt: 0}
		s2 := &manmanpb.Session{SessionId: 9, StartedAt: 0}
		s3 := &manmanpb.Session{SessionId: 2, StartedAt: 0}

		got := LatestSession([]*manmanpb.Session{s1, s2, s3})
		if got == nil || got.GetSessionId() != 9 {
			t.Errorf("LatestSession(all started_at=0) = %v, want session_id=9", got)
		}
	})

	t.Run("tied started_at", func(t *testing.T) {
		s1 := &manmanpb.Session{SessionId: 5, StartedAt: 150}
		s2 := &manmanpb.Session{SessionId: 9, StartedAt: 150}
		s3 := &manmanpb.Session{SessionId: 2, StartedAt: 150}

		got := LatestSession([]*manmanpb.Session{s1, s2, s3})
		if got == nil || got.GetSessionId() != 9 {
			t.Errorf("LatestSession(tied started_at) = %v, want session_id=9", got)
		}
	})
}

// TestLatestSession_DoesNotMutateInputOrder covers the "input slice order is
// unchanged after the call" requirement of test 3: the caller's ordering is
// not guaranteed to be meaningful, and LatestSession must not reorder it.
func TestLatestSession_DoesNotMutateInputOrder(t *testing.T) {
	s1 := &manmanpb.Session{SessionId: 1, StartedAt: 300}
	s2 := &manmanpb.Session{SessionId: 2, StartedAt: 100}
	s3 := &manmanpb.Session{SessionId: 3, StartedAt: 200}

	sessions := []*manmanpb.Session{s1, s2, s3}
	_ = LatestSession(sessions)

	wantOrder := []int64{1, 2, 3}
	for i, s := range sessions {
		if s.GetSessionId() != wantOrder[i] {
			t.Fatalf("input slice order changed: index %d = session_id %d, want %d", i, s.GetSessionId(), wantOrder[i])
		}
	}
}

// TestComputeConnectAddresses_SingleBinding covers test 4.
func TestComputeConnectAddresses_SingleBinding(t *testing.T) {
	bindings := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}

	got := ComputeConnectAddresses("play.example.com", bindings)
	if len(got) != 1 {
		t.Fatalf("ComputeConnectAddresses(single binding) returned %d entries, want 1", len(got))
	}
	want := ConnectAddress{Address: "play.example.com:25565", Port: 25565, Protocol: "TCP"}
	if got[0] != want {
		t.Errorf("ComputeConnectAddresses(single binding)[0] = %+v, want %+v", got[0], want)
	}
}

// TestComputeConnectAddresses_MultipleBindings covers test 5: one entry per
// binding, in binding order, all sharing the same host.
func TestComputeConnectAddresses_MultipleBindings(t *testing.T) {
	bindings := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		{ContainerPort: 19132, HostPort: 19132, Protocol: "UDP"},
		{ContainerPort: 8080, HostPort: 8080, Protocol: "TCP"},
	}

	got := ComputeConnectAddresses("play.example.com", bindings)
	want := []ConnectAddress{
		{Address: "play.example.com:25565", Port: 25565, Protocol: "TCP"},
		{Address: "play.example.com:19132", Port: 19132, Protocol: "UDP"},
		{Address: "play.example.com:8080", Port: 8080, Protocol: "TCP"},
	}

	if len(got) != len(want) {
		t.Fatalf("ComputeConnectAddresses(multiple bindings) returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ComputeConnectAddresses(multiple bindings)[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestComputeConnectAddresses_EmptyHostReturnsNil covers test 6 (FR7): an
// empty host_public_address must never produce a malformed ":25565" address,
// so the caller renders "connect address unavailable" instead.
func TestComputeConnectAddresses_EmptyHostReturnsNil(t *testing.T) {
	bindings := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}
	got := ComputeConnectAddresses("", bindings)
	if got != nil {
		t.Errorf("ComputeConnectAddresses(empty host) = %+v, want nil", got)
	}
}

// TestComputeConnectAddresses_NoBindingsReturnsNil covers test 7.
func TestComputeConnectAddresses_NoBindingsReturnsNil(t *testing.T) {
	got := ComputeConnectAddresses("play.example.com", nil)
	if got != nil {
		t.Errorf("ComputeConnectAddresses(nil bindings) = %+v, want nil", got)
	}
}

// TestComputeConnectAddresses_HostPassthroughNoNormalisation covers test 8:
// M1 does no validation or normalisation of host_public_address, including
// IPv6 literals and host-with-port-looking values. This pins the current
// pass-through behaviour explicitly so a future change to add validation or
// normalisation is a deliberate one, not an accidental regression.
func TestComputeConnectAddresses_HostPassthroughNoNormalisation(t *testing.T) {
	bindings := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}

	cases := []struct {
		name string
		host string
		want string
	}{
		{"ipv6 literal", "2001:db8::1", "2001:db8::1:25565"},
		{"host with port-looking suffix", "play.example.com:8443", "play.example.com:8443:25565"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeConnectAddresses(tc.host, bindings)
			if len(got) != 1 {
				t.Fatalf("ComputeConnectAddresses(%q) returned %d entries, want 1", tc.host, len(got))
			}
			if got[0].Address != tc.want {
				t.Errorf("ComputeConnectAddresses(%q)[0].Address = %q, want %q", tc.host, got[0].Address, tc.want)
			}
		})
	}
}
