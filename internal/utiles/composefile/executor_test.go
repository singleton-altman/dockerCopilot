package composefile

import (
	"os"
	"path/filepath"
	"testing"
)

func newExecutor(t *testing.T) *Executor {
	t.Helper()
	return NewExecutor(t.TempDir())
}

func TestSubmitCreatesJob(t *testing.T) {
	ex := newExecutor(t)

	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	entries, err := os.ReadDir(filepath.Join(ex.workDir, QueueDirName))
	if err != nil {
		t.Fatalf("queue dir missing: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != id+".json" {
		t.Fatalf("unexpected queue contents: %v", entries)
	}

	// 不应残留临时文件
	if _, err := os.Stat(filepath.Join(ex.workDir, QueueDirName, id+".json.tmp")); err == nil {
		t.Fatal("temporary file left behind")
	}
}

func TestSubmitRejectsInvalidAction(t *testing.T) {
	ex := newExecutor(t)
	if _, err := ex.Submit("web.yaml", Action("rm -rf /")); err == nil {
		t.Fatal("expected invalid action to be rejected")
	}
}

func TestSubmitRejectsWhenQueueFull(t *testing.T) {
	ex := newExecutor(t)
	for i := 0; i < ex.maxPending; i++ {
		if _, err := ex.Submit("web.yaml", ActionUp); err != nil {
			t.Fatalf("unexpected error at %d: %v", i, err)
		}
	}
	if _, err := ex.Submit("web.yaml", ActionUp); err == nil {
		t.Fatal("expected queue-full error")
	}
}

func TestResultNotReady(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	result, done, err := ex.Result(id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if done {
		t.Fatal("expected result to be pending")
	}
	if result.ID != "" {
		t.Fatalf("expected empty result, got %+v", result)
	}
}

func TestResultReadsWatcherOutput(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	payload := `{"id":"` + id + `","filename":"web.yaml","action":"up","success":true,"output":"done","finishedAt":"2026-10-19T10:00:00Z"}`
	if err := os.MkdirAll(filepath.Join(ex.workDir, ResultDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ex.workDir, ResultDirName, id+".json"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	result, done, err := ex.Result(id)
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	if !done || !result.Success || result.Output != "done" {
		t.Fatalf("unexpected result: done=%v %+v", done, result)
	}
}

func TestResultRejectsUnsafeID(t *testing.T) {
	ex := newExecutor(t)
	for _, id := range []string{"", "../escape", "a/b", "..", "id with space"} {
		if _, _, err := ex.Result(id); err == nil {
			t.Errorf("expected Result(%q) to fail", id)
		}
	}
}

func TestRecentReturnsNewestFirst(t *testing.T) {
	ex := newExecutor(t)
	dir := filepath.Join(ex.workDir, ResultDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a.json": `{"id":"a","success":true,"finishedAt":"2026-10-19T10:00:00Z"}`,
		"b.json": `{"id":"b","success":true,"finishedAt":"2026-10-19T12:00:00Z"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	results, err := ex.Recent(10)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if len(results) != 2 || results[0].ID != "b" {
		t.Fatalf("unexpected order: %+v", results)
	}
}
