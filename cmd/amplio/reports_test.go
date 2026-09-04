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

package main

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"

	"amplio/internal/agent/critic"
	"amplio/internal/config"
	"amplio/internal/db"
	"amplio/internal/db/sqlite"
	"amplio/internal/llm"
)

// stubHQ never calls submit_report, so GenerateReport falls back to a sentinel
// summary — enough to verify backfill writes a report per concluded run.
type stubHQ struct{}

func (stubHQ) Call(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: "no report"}, nil
}
func (stubHQ) Stream(context.Context, llm.Request) (llm.Stream, error) { return nil, nil }
func (stubHQ) ModelID() string                                         { return "stub" }
func (stubHQ) MaxTokens() int                                          { return 1000 }

func TestBackfillReports(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// A concluded autonomous run with no report yet.
	if err := store.CreateRun(ctx, db.RunRecord{
		RunID:  "r",
		Config: config.RunConfig{Task: "t"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, db.SessionRecord{
		RunID: "r", SessionID: "main-agent", AgentType: "standard_agent", Status: db.SessionConcluded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceStep(ctx, "r", "main-agent"); err != nil {
		t.Fatal(err)
	}

	fin := critic.NewFinalizer(store, stubHQ{}, nil, nil, nil)

	backfillReports(ctx, store, fin)
	reports, err := critic.AllReports(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("backfill produced %d reports, want 1", len(reports))
	}

	// Idempotent: a second pass adds nothing (watermark covered).
	backfillReports(ctx, store, fin)
	reports, err = critic.AllReports(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("second backfill produced %d reports, want 1", len(reports))
	}
}

func TestExecuteResume_BackfillsMissingReportForRestingRun(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	config.SetDataDir(dataDir)
	t.Cleanup(func() { config.SetDataDir("") })

	dbPath := filepath.Join(dataDir, "amplio.db")
	store, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(ctx, db.RunRecord{
		RunID:  "r",
		Config: config.RunConfig{Task: "t"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, db.SessionRecord{
		RunID: "r", SessionID: config.RootAgentSessionID,
		AgentType: "standard_agent", Status: db.SessionConcluded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceStep(ctx, "r", config.RootAgentSessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	const providerName = "resume-test"
	old, hadOld := providerRegistry[providerName]
	providerRegistry[providerName] = providerEntry{
		new: func(_ string, _ int, _, _ url.Values) (llm.Provider, error) {
			return stubHQ{}, nil
		},
	}
	t.Cleanup(func() {
		if hadOld {
			providerRegistry[providerName] = old
		} else {
			delete(providerRegistry, providerName)
		}
	})

	cfg := config.Config{
		DB:            dbPath,
		SystemLLMHQ:   providerName + ":hq",
		SystemLLMFast: providerName + ":fast",
	}
	if err := executeResume(cfg, "r"); err != nil {
		t.Fatalf("executeResume: %v", err)
	}

	store, err = sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	reports, err := critic.AllReports(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("resume produced %d reports, want 1", len(reports))
	}
}
