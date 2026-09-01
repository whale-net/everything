package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// stubManManAPIClient embeds the (nil) manmanpb.ManManAPIClient interface
// so it satisfies the full interface without implementing every RPC --
// only updateServerFunc is exercised by these tests, and any other method
// call would nil-panic, which is the desired failure mode for an
// unexpected call.
type stubManManAPIClient struct {
	manmanpb.ManManAPIClient
	updateServerFunc func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error)
}

func (s *stubManManAPIClient) UpdateServer(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
	return s.updateServerFunc(ctx, in, opts...)
}

// TestHandleServerUpdateAddress_SendsFieldMask guards #1528's clear-via-
// field-mask contract (per #1527): both a non-empty and an empty submitted
// value must send update_paths == ["host_public_address"] on the outgoing
// UpdateServerRequest, so an empty submission clears the field via the
// mask rather than falling back to update-all semantics (which would wipe
// every other field on the server).
func TestHandleServerUpdateAddress_SendsFieldMask(t *testing.T) {
	cases := []struct {
		name        string
		submitted   string
		wantAddress string
	}{
		{name: "non-empty value", submitted: "game.example.com:27015", wantAddress: "game.example.com:27015"},
		{name: "empty value clears", submitted: "", wantAddress: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq *manmanpb.UpdateServerRequest
			stub := &stubManManAPIClient{
				updateServerFunc: func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
					gotReq = in
					return &manmanpb.UpdateServerResponse{}, nil
				},
			}
			app := &App{grpc: &ControlClient{api: stub}}

			form := url.Values{"host_public_address": {tc.submitted}}
			req := httptest.NewRequest(http.MethodPost, "/servers/6/update-address", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			app.handleServerUpdateAddress(w, req, "6")

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
			}
			if got, want := w.Header().Get("Location"), "/servers/6"; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}

			if gotReq == nil {
				t.Fatalf("expected UpdateServer to be called")
			}
			if gotReq.ServerId != 6 {
				t.Errorf("ServerId = %d, want 6", gotReq.ServerId)
			}
			if gotReq.HostPublicAddress != tc.wantAddress {
				t.Errorf("HostPublicAddress = %q, want %q", gotReq.HostPublicAddress, tc.wantAddress)
			}
			wantPaths := []string{"host_public_address"}
			if len(gotReq.UpdatePaths) != len(wantPaths) || gotReq.UpdatePaths[0] != wantPaths[0] {
				t.Errorf("UpdatePaths = %v, want %v", gotReq.UpdatePaths, wantPaths)
			}
		})
	}
}

// TestHandleServerUpdateAddress_RejectsNonPost guards NFR2/FR10-adjacent
// hygiene: this handler must only accept POST (matching every other
// mutating /servers route), not silently accept a GET that could be
// triggered by a prefetch or a stray link.
func TestHandleServerUpdateAddress_RejectsNonPost(t *testing.T) {
	called := false
	stub := &stubManManAPIClient{
		updateServerFunc: func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
			called = true
			return &manmanpb.UpdateServerResponse{}, nil
		},
	}
	app := &App{grpc: &ControlClient{api: stub}}

	req := httptest.NewRequest(http.MethodGet, "/servers/6/update-address", nil)
	w := httptest.NewRecorder()

	app.handleServerUpdateAddress(w, req, "6")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if called {
		t.Errorf("expected UpdateServer not to be called for a GET request")
	}
}
