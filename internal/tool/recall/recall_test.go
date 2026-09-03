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

package recall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amplio/internal/db"
	"amplio/internal/db/sqlite"
	"amplio/internal/embed"
	"amplio/internal/lessons"
	"amplio/internal/skills"
)

func buildIndexes(t *testing.T) (*skills.Index, *lessons.Index, db.Store) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	for name, desc := range map[string]string{
		"spanner": "query spanner sql databases",
		"gmail":   "read and send email messages",
	} {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\nguide body for " + name
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emb := embed.Mock{Dim: 4096}
	skillIx := skills.NewIndex([]skills.Source{{Name: "t", Path: dir}}, emb, skills.NewDBCache(store))
	if err := skillIx.Build(ctx); err != nil {
		t.Fatal(err)
	}

	vecs, err := emb.Embed(ctx, []string{"flaky build retry workaround"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertLesson(ctx, db.LessonRecord{
		LessonID: "abc123", Title: "Retry flaky builds", Description: "flaky build retry workaround",
		Body: "rerun with --flaky_test_attempts", Embedding: vecs[0], EmbedderID: emb.ModelID(), SourceRunID: "run-1",
	}); err != nil {
		t.Fatal(err)
	}
	lessonIx := lessons.NewIndex(store, emb)
	if err := lessonIx.Build(ctx); err != nil {
		t.Fatal(err)
	}
	return skillIx, lessonIx, store
}

func TestRecallSearch(t *testing.T) {
	skillIx, lessonIx, _ := buildIndexes(t)
	ctx := context.Background()

	res, err := Search(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"query":"query spanner sql"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "skill:spanner") {
		t.Fatalf("search missing spanner: %s", res.Content)
	}

	res, err = Search(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"query":"flaky build retry"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "lesson:abc123") || !strings.Contains(res.Content, "Lessons (mined") {
		t.Fatalf("search missing lesson: %s", res.Content)
	}
}

func TestRecallLoad(t *testing.T) {
	skillIx, lessonIx, store := buildIndexes(t)
	ctx := context.Background()

	res, err := Load(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"handle":"skill:spanner"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "# Skill: spanner") || !strings.Contains(res.Content, "guide body") {
		t.Fatalf("load skill body wrong: %s", res.Content)
	}

	res, err = Load(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"handle":"lesson:abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "# Lesson: Retry flaky builds") || !strings.Contains(res.Content, "rerun with") {
		t.Fatalf("load lesson body wrong: %s", res.Content)
	}
	// recall_load bumps the lesson's load counter.
	l, err := store.GetLesson(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if l.LoadedCount != 1 {
		t.Fatalf("loaded_count = %d, want 1", l.LoadedCount)
	}

	// Unknown handle prefix → error result.
	res, err = Load(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"handle":"bogus:x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("expected error for unknown handle: %s", res.Content)
	}
}

func TestRecallInitialContent(t *testing.T) {
	skillIx, lessonIx, _ := buildIndexes(t)
	ctx := context.Background()

	if c := InitialContent(ctx, skillIx, lessonIx, "query spanner sql"); !strings.Contains(c, "skill:spanner") {
		t.Fatalf("initial content missing skill: %s", c)
	}
	if c := InitialContent(ctx, skillIx, lessonIx, "flaky build retry"); !strings.Contains(c, "lesson:abc123") {
		t.Fatalf("initial content missing lesson: %s", c)
	}
	if c := InitialContent(ctx, skillIx, lessonIx, ""); c != "" {
		t.Fatalf("empty task should yield no initial content, got %q", c)
	}
}

// The instance-wide isolation switch: no lesson reaches an agent through any of
// the three surfaces, while the index itself still answers — that is what
// end-of-run mining, its near-duplicate check, lesson scoring and the
// operator's /recall page run on, and they must keep working.
func TestLessonSearchDisabled(t *testing.T) {
	skillIx, lessonIx, _ := buildIndexes(t)
	ctx := context.Background()
	lessonIx.DisableRecall()

	// 1. recall_search: skills still hit, the lesson never does.
	res, err := Search(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"query":"flaky build retry"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "lesson:abc123") || strings.Contains(res.Content, "Lessons (mined") {
		t.Errorf("lesson leaked into search: %s", res.Content)
	}
	res, err = Search(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"query":"query spanner sql"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "skill:spanner") {
		t.Errorf("skills must still be searchable: %s", res.Content)
	}

	// 2. The tool description must not advertise a corpus it will never return.
	if d := Search(skillIx, lessonIx).Description; strings.Contains(d, "lessons") {
		t.Errorf("description still promises lessons: %s", d)
	}

	// 3. recall_load of a lesson handle is refused, and says why (a bare
	// "unavailable" reads as a transient fault worth retrying).
	res, err = Load(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"handle":"lesson:abc123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content, "disabled") {
		t.Errorf("lesson load = %q (error=%v), want a refusal naming the switch", res.Content, res.IsError)
	}
	if strings.Contains(res.Content, "rerun with") {
		t.Errorf("lesson body leaked through recall_load: %s", res.Content)
	}
	// Skills load as usual.
	res, err = Load(skillIx, lessonIx).Execute(ctx, json.RawMessage(`{"handle":"skill:spanner"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "# Skill: spanner") {
		t.Errorf("skill load broke: %s", res.Content)
	}

	// 4. The run-start seed carries skills only.
	if c := InitialContent(ctx, skillIx, lessonIx, "flaky build retry"); strings.Contains(c, "lesson:abc123") {
		t.Errorf("lesson leaked into the initial recall seed: %s", c)
	}
	if c := InitialContent(ctx, skillIx, lessonIx, "query spanner sql"); !strings.Contains(c, "skill:spanner") {
		t.Errorf("skills must still seed: %s", c)
	}

	// 5. Mining/scoring/UI path: the index itself is untouched.
	hits, err := lessonIx.Search(ctx, "flaky build retry", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Error("the index stopped answering — mining, dedup and scoring depend on it")
	}
	if !lessonIx.IsBuilt() {
		t.Error("index reports not built")
	}
}
