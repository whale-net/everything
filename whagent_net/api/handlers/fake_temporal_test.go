//go:build integration

// fakeTemporalClient is a hermetic, in-process double for
// temporalclient.Client, standing in for a live Temporal server the same
// way stubTransport (whagent_net/llm/client_test.go) stands in for a live
// OpenRouter -- see worker/replay_test.go's historyFixtureBuilder doc
// comment for why this repo does not take a live-Temporal-server test
// dependency anywhere, including here. It records every ExecuteWorkflow/
// SignalWorkflow call StartSession/SendTurn/StopSession make so a test can
// assert on workflow ID, signal name, and signal payload without a real
// Temporal frontend.
//
// Deliberately implements only ExecuteWorkflow and SignalWorkflow --
// temporalclient.Client is embedded as a nil interface value, so every
// other method (GetWorkflow, CancelWorkflow, QueryWorkflow, ...) panics on
// a nil pointer dereference if called. That is intentional, not an
// oversight: SendTurn's asynchronous contract means the handler must never
// reach for a wait-for-completion API on the client at all -- if a future
// edit adds one, this fake makes that regression fail loudly here rather
// than silently changing SendTurn's contract.
package handlers_test

import (
	"context"
	"sync"

	temporalclient "go.temporal.io/sdk/client"
)

// startedWorkflow records one ExecuteWorkflow call.
type startedWorkflow struct {
	Options  temporalclient.StartWorkflowOptions
	Workflow interface{}
	Args     []interface{}
}

// signalCall records one SignalWorkflow call.
type signalCall struct {
	WorkflowID string
	RunID      string
	SignalName string
	Arg        interface{}
}

// fakeTemporalClient is the fake described in this file's package doc
// comment above.
type fakeTemporalClient struct {
	temporalclient.Client // nil; every unoverridden method panics if called

	mu      sync.Mutex
	started []startedWorkflow
	signals []signalCall
}

func (f *fakeTemporalClient) ExecuteWorkflow(ctx context.Context, options temporalclient.StartWorkflowOptions, workflow interface{}, args ...interface{}) (temporalclient.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, startedWorkflow{Options: options, Workflow: workflow, Args: args})
	return &fakeWorkflowRun{id: options.ID}, nil
}

func (f *fakeTemporalClient) SignalWorkflow(ctx context.Context, workflowID string, runID string, signalName string, arg interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, signalCall{WorkflowID: workflowID, RunID: runID, SignalName: signalName, Arg: arg})
	return nil
}

// startedSnapshot returns a copy of every ExecuteWorkflow call recorded so
// far -- a copy so a test can range over it without racing a concurrent
// call into the fake.
func (f *fakeTemporalClient) startedSnapshot() []startedWorkflow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]startedWorkflow, len(f.started))
	copy(out, f.started)
	return out
}

// signalsSnapshot is startedSnapshot's counterpart for SignalWorkflow calls.
func (f *fakeTemporalClient) signalsSnapshot() []signalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]signalCall, len(f.signals))
	copy(out, f.signals)
	return out
}

// fakeWorkflowRun is the temporalclient.WorkflowRun ExecuteWorkflow returns
// -- StartSession (start.go) never calls any of its methods (it only
// checks the error ExecuteWorkflow itself returns), so these are never
// exercised by the handler under test. Like fakeTemporalClient itself, it
// embeds a nil temporalclient.WorkflowRun so every method besides GetID/
// GetRunID/Get panics if ever called, rather than silently satisfying the
// (larger, GetWithOptions-bearing) interface with a fabricated no-op.
type fakeWorkflowRun struct {
	temporalclient.WorkflowRun
	id string
}

func (r *fakeWorkflowRun) GetID() string    { return r.id }
func (r *fakeWorkflowRun) GetRunID() string { return "" }
func (r *fakeWorkflowRun) Get(ctx context.Context, valuePtr interface{}) error {
	return nil
}
