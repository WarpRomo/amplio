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
	"strings"
	"testing"

	"amplio/internal/config"
)

func TestEnabled(t *testing.T) {
	const spec = "vertex-claude:claude-opus-5?output_config.effort=xhigh"
	on := config.ResponseRewrite{Model: "vertex-gemini:gemini-3.7-flash", For: []string{"opus-5 · xhigh"}}

	for _, tc := range []struct {
		name string
		cfg  config.ResponseRewrite
		spec string
		want bool
	}{
		// The label is what the UI shows, so it is what an operator will paste.
		{"short label", on, spec, true},
		{"full spec", config.ResponseRewrite{Model: "m", For: []string{spec}}, spec, true},
		{"nickname", config.ResponseRewrite{Model: "m", For: []string{"prod"}}, spec + "#prod", true},
		{"spec matches even when the run carries a nickname",
			config.ResponseRewrite{Model: "m", For: []string{spec}}, spec + "#prod", true},

		// Opt-in means opt-in: every way of saying "not configured" is off.
		{"no for list", config.ResponseRewrite{Model: "m"}, spec, false},
		{"no model", config.ResponseRewrite{For: []string{"opus-5 · xhigh"}}, spec, false},
		{"empty entries", config.ResponseRewrite{Model: "m", For: []string{"", "  "}}, spec, false},
		{"run has no model", on, "", false},

		// No wildcards, and no accidental prefix matching: a bare family name
		// would quietly enrol every variant, including ones never considered.
		{"bare family name does not match", config.ResponseRewrite{Model: "m", For: []string{"opus-5"}}, spec, false},
		{"different model", on, "vertex-gemini:gemini-3.7-flash", false},
	} {
		if got := Enabled(tc.cfg, tc.spec); got != tc.want {
			t.Errorf("%s: Enabled(%q) = %v, want %v", tc.name, tc.spec, got, tc.want)
		}
	}
}

func TestPrompt(t *testing.T) {
	def := Prompt(config.ResponseRewrite{})
	if !strings.Contains(def, "Output ONLY the rewritten Markdown") {
		t.Errorf("built-in prompt looks wrong: %q", def[:min(80, len(def))])
	}
	if strings.HasSuffix(def, "\n") {
		t.Error("prompt should be trimmed")
	}
	// An override replaces the built-in wholesale — it is not appended, so an
	// operator cannot end up with two sets of instructions fighting.
	got := Prompt(config.ResponseRewrite{Prompt: "  just do it  "})
	if got != "just do it" {
		t.Errorf("override = %q", got)
	}
}
