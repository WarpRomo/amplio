// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package responserewrite

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"amplio/internal/config"
	"amplio/internal/db"
	"amplio/internal/db/sqlite"
	"amplio/internal/event"
	"amplio/internal/llm"
)

const spec = "vertex-gemini:gemini-3.7-flash"

type fixture struct {
	store  db.Store
	rw     *Rewriter
	calls  *atomic.Int32
	bumps  *atomic.Int32
	runID  string
	sessID string
}

func setup(t *testing.T, agentType, runLLM string, cfg config.ResponseRewrite) fixture {
	t.Helper()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const runID, sessID = "run1", "chatty-bot"
	if err := store.CreateRun(ctx, db.RunRecord{RunID: runID, Config: config.RunConfig{LLM: runLLM}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, db.SessionRecord{
		RunID: runID, SessionID: sessID, AgentType: agentType, Status: db.SessionIdle,
	}); err != nil {
		t.Fatal(err)
	}
	calls, bumps := &atomic.Int32{}, &atomic.Int32{}
	provider := func(string) (llm.Provider, error) {
		calls.Add(1)
		return &llm.MockProvider{Model: "m", Responses: []llm.Response{{Content: "PLAIN VERSION"}}}, nil
	}
	rw := New(cfg, store, provider, func(string, string) { bumps.Add(1) })
	return fixture{store: store, rw: rw, calls: calls, bumps: bumps, runID: runID, sessID: sessID}
}

func enabled() config.ResponseRewrite {
	return config.ResponseRewrite{Model: spec, For: []string{spec}}
}

// waitFor polls instead of sleeping: the rewrite runs in its own goroutine, and
// a fixed sleep is either flaky or slow.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// Driven through the STORE, not by calling Observe with a hand-made event: the
// step a rewrite is filed under comes from the event's ColumnFields, which only
// the store fills in. An earlier version of this test set Step by hand and so
// passed while every real rewrite was being stored at step 0 — invisible in the
// chat, which joins the rewrite to a bubble by step.
func TestObserve_RewritesAConclusion(t *testing.T) {
	f := setup(t, "chatbot", spec, enabled())
	f.store.SetCommitListener(f.rw.Observe)
	const at = 4
	if err := f.store.FinalizeStep(context.Background(), f.runID, f.sessID, at,
		[]event.Event{&event.AssistantEvent{Content: "dense original prose"}}); err != nil {
		t.Fatal(err)
	}
	var got map[int]db.ResponseRewriteRecord
	if !waitFor(t, func() bool {
		got, _ = f.store.ListResponseRewrites(context.Background(), f.runID, f.sessID)
		return len(got) > 0
	}) {
		t.Fatal("no rewrite stored")
	}
	if _, ok := got[at]; !ok {
		steps := make([]int, 0, len(got))
		for s := range got {
			steps = append(steps, s)
		}
		t.Fatalf("rewrite filed at steps %v, want %d", steps, at)
	}
	rec := got[at]
	if rec.Text != "PLAIN VERSION" {
		t.Errorf("text = %q", rec.Text)
	}
	if rec.Model != spec {
		t.Errorf("model = %q, want the rewriting model recorded", rec.Model)
	}
	// The rewrite is not an event, so without a bump the open page never learns.
	if !waitFor(t, func() bool { return f.bumps.Load() == 1 }) {
		t.Errorf("ui bumps = %d, want 1", f.bumps.Load())
	}
}

// Everything that must NOT trigger a billable call.
func TestObserve_Skips(t *testing.T) {
	conclusion := &event.AssistantEvent{ColumnFields: event.ColumnFields{Step: 1}, Content: "text"}
	for _, tc := range []struct {
		name      string
		agentType string
		runLLM    string
		cfg       config.ResponseRewrite
		evt       event.Event
	}{
		{"tool-calling turn is not a conclusion", "chatbot", spec, enabled(),
			&event.AssistantEvent{ColumnFields: event.ColumnFields{Step: 1}, Content: "x",
				ToolCalls: []event.ToolCall{{ID: "t1", Name: "bash"}}}},
		{"empty content", "chatbot", spec, enabled(),
			&event.AssistantEvent{ColumnFields: event.ColumnFields{Step: 1}, Content: "   "}},
		{"not an assistant turn", "chatbot", spec, enabled(),
			&event.UserEvent{ColumnFields: event.ColumnFields{Step: 1}, Content: "hello"}},
		{"non-chatbot session", "standard_agent", spec, enabled(), conclusion},
		{"run model not opted in", "chatbot", "vertex-gemini:gemini-3.1-pro-preview", enabled(), conclusion},
		{"feature off", "chatbot", spec, config.ResponseRewrite{}, conclusion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.agentType, tc.runLLM, tc.cfg)
			f.rw.Observe(f.runID, f.sessID, tc.evt)
			// Give a would-be goroutine time to misbehave before declaring success.
			time.Sleep(120 * time.Millisecond)
			if n := f.calls.Load(); n != 0 {
				t.Errorf("provider called %d times, want 0", n)
			}
			got, _ := f.store.ListResponseRewrites(context.Background(), f.runID, f.sessID)
			if len(got) != 0 {
				t.Errorf("stored %d rewrites, want none", len(got))
			}
		})
	}
}
