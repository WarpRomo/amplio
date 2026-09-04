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

// Package responserewrite restates an agent's conclusion messages in plainer
// prose, stored beside the original so the UI can offer both.
//
// Deliberately small. One LLM call per conclusion, fired and forgotten: a
// missing rewrite shows the original with no toggle, which is the same thing
// the operator sees before opting in. There is no retry beyond the provider's
// own, no queue, no resume after restart, and no automatic quality check —
// an offline experiment showed a judge cannot reliably detect a rewrite that
// dropped a fact, so pretending otherwise would be worse than not checking.
package responserewrite

import (
	_ "embed"
	"strings"

	"amplio/internal/config"
	"amplio/internal/llm"
)

//go:embed prompts/rewrite.md
var defaultPrompt string

// Prompt is the system prompt for a rewrite: the operator's override when set,
// else the built-in. Embedded in-tree rather than defaulted in config so that
// changing it goes through review with the rest of the code.
func Prompt(cfg config.ResponseRewrite) string {
	if p := strings.TrimSpace(cfg.Prompt); p != "" {
		return p
	}
	return strings.TrimSpace(defaultPrompt)
}

// Enabled reports whether a run using runSpec opts in.
//
// An entry matches the run's spec exactly, or its #nickname, or its short
// label — the three ways a model is already named elsewhere (a bridge handle
// resolves the same way), so an operator can paste what the UI shows. No
// wildcards: "opus-5" matching every opus-5 variant would silently include
// models the operator never considered, and the menu already gives exact names.
func Enabled(cfg config.ResponseRewrite, runSpec string) bool {
	if len(cfg.For) == 0 || cfg.Model == "" || strings.TrimSpace(runSpec) == "" {
		return false
	}
	base, nickname := llm.SplitNickname(runSpec)
	label := llm.ShortLabel(runSpec)
	for _, want := range cfg.For {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if want == strings.TrimSpace(runSpec) || want == base || (nickname != "" && want == nickname) || want == label {
			return true
		}
	}
	return false
}
