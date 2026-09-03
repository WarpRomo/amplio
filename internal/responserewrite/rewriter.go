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
	"log/slog"
	"strings"
	"time"

	"amplio/internal/config"
	"amplio/internal/db"
	"amplio/internal/event"
	"amplio/internal/llm"
)

// callTimeout bounds one rewrite. Generous: the offline runs took a median of
// 26s and up to 39s on the larger models. Nothing waits on this, so a slow
// rewrite costs nothing but the goroutine.
const callTimeout = 3 * time.Minute

// Rewriter turns committed conclusion messages into plain-prose restatements.
//
// Fire and forget by design: Observe returns immediately, the work happens in
// its own goroutine on a background context (the commit listener's caller must
// not wait, and a rewrite must outlive the request that triggered it), and
// every failure is logged and dropped. A missing rewrite is invisible in the
// UI — the same thing the reader sees before opting in.
type Rewriter struct {
	cfg      config.ResponseRewrite
	store    db.Store
	provider func(spec string) (llm.Provider, error)
	notify   func(runID, sessionID string) // publish a UI bump; nil to skip
	prompt   string
}

func New(cfg config.ResponseRewrite, store db.Store,
	provider func(spec string) (llm.Provider, error),
	notify func(runID, sessionID string)) *Rewriter {
	return &Rewriter{cfg: cfg, store: store, provider: provider, notify: notify, prompt: Prompt(cfg)}
}

// Observe is a db.CommitListener fragment: cheap, synchronous, and silent
// unless this event is a conclusion worth rewriting.
//
// The gate order is deliberate — the free checks (event shape) come before the
// two that read the DB, so the overwhelming majority of commits cost nothing.
func (r *Rewriter) Observe(runID, sessionID string, evt event.Event) {
	if r == nil || len(r.cfg.For) == 0 {
		return
	}
	a, ok := evt.(*event.AssistantEvent)
	// A conclusion is an assistant turn with no tool calls: the message the chat
	// UI shows as the end of a turn. Intermediate turns are working notes.
	if !ok || len(a.ToolCalls) > 0 || strings.TrimSpace(a.Content) == "" {
		return
	}
	// Read everything the goroutine needs HERE, while the appending caller is
	// still blocked in the store: the event belongs to that caller, so the
	// background rewrite must not hold a pointer into it. Step is stamped by
	// the store at commit time (see db.CommitListener).
	go r.rewrite(runID, sessionID, a.Step, a.Content)
}

func (r *Rewriter) rewrite(runID, sessionID string, step int, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	log := slog.With("run_id", runID, "session_id", sessionID, "step", step)

	sess, err := r.store.GetSession(ctx, runID, sessionID)
	if err != nil || sess.AgentType != "chatbot" {
		return // only the operator-facing session, for now
	}
	run, err := r.store.GetRun(ctx, runID)
	if err != nil || !Enabled(r.cfg, run.Config.LLM) {
		return
	}

	p, err := r.provider(r.cfg.Model)
	if err != nil {
		log.Warn("response rewrite: provider", "model", r.cfg.Model, "error", err)
		return
	}
	resp, err := p.Call(ctx, llm.Request{
		SystemPrompt: r.prompt,
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: content}},
	})
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		log.Warn("response rewrite: call", "error", err)
		return
	}
	if err := r.store.PutResponseRewrite(ctx, db.ResponseRewriteRecord{
		RunID: runID, SessionID: sessionID, Step: step,
		Model: r.cfg.Model, Text: resp.Content,
	}); err != nil {
		log.Warn("response rewrite: store", "error", err)
		return
	}
	// The rewrite is not an event, so nothing else tells the page it exists.
	if r.notify != nil {
		r.notify(runID, sessionID)
	}
	log.Debug("response rewrite stored", "chars_in", len(content), "chars_out", len(resp.Content))
}
