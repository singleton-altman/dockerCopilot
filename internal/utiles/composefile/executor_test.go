package composefile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func queueJob(t *testing.T, ex *Executor, job Job) {
	t.Helper()
	content, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ex.workDir, QueueDirName, job.ID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerExecutesQueuedJob(t *testing.T) {
	ex := newExecutor(t)
	ex.runner = func(ctx context.Context, job Job) JobResult {
		return JobResult{Success: true, Output: "ran " + string(job.Action)}
	}
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	ex.drainOnce(context.Background())

	entries, err := os.ReadDir(filepath.Join(ex.workDir, QueueDirName))
	if err != nil {
		t.Fatalf("queue dir missing: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("queue not drained: %v", entry.Name())
		}
	}

	result, done, err := ex.Result(id)
	if err != nil || !done {
		t.Fatalf("expected finished result, done=%v err=%v", done, err)
	}
	if !result.Success || result.Output != "ran up" || result.Filename != "web.yaml" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestConsumerSkipsExpiredJob(t *testing.T) {
	ex := newExecutor(t)
	ex.runner = func(ctx context.Context, job Job) JobResult {
		t.Fatal("expired job must not run")
		return JobResult{}
	}
	job := Job{ID: "1-expired-web", Filename: "web.yaml", Action: ActionUp,
		CreatedAt: time.Now().Add(-2 * jobMaxAge)}
	queueJob(t, ex, job)

	ex.drainOnce(context.Background())

	result, done, err := ex.Result(job.ID)
	if err != nil || !done {
		t.Fatalf("expected finished result, done=%v err=%v", done, err)
	}
	if result.Success || !strings.Contains(result.Output, "过期") {
		t.Fatalf("expected expired failure, got %+v", result)
	}
}

func TestConsumerRejectsInvalidJob(t *testing.T) {
	ex := newExecutor(t)
	ex.runner = func(ctx context.Context, job Job) JobResult {
		t.Fatal("invalid job must not run")
		return JobResult{}
	}
	job := Job{ID: "1-bad-web", Filename: "../escape.yaml", Action: ActionUp,
		CreatedAt: time.Now()}
	queueJob(t, ex, job)

	ex.drainOnce(context.Background())

	result, done, err := ex.Result(job.ID)
	if err != nil || !done {
		t.Fatalf("expected finished result, done=%v err=%v", done, err)
	}
	if result.Success {
		t.Fatalf("expected rejection, got %+v", result)
	}

	job = Job{ID: "1-bad2-web", Filename: "web.yaml", Action: Action("rm -rf /"),
		CreatedAt: time.Now()}
	queueJob(t, ex, job)
	ex.drainOnce(context.Background())
	result, done, err = ex.Result(job.ID)
	if err != nil || !done || result.Success {
		t.Fatalf("expected action rejection, done=%v result=%+v err=%v", done, result, err)
	}
}

func TestConsumerSkipsJobClaimedByOthers(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	// 模拟外部 watcher 先把任务取走
	queuePath := filepath.Join(ex.workDir, QueueDirName, id+".json")
	if err := os.Rename(queuePath, queuePath+".claim"); err != nil {
		t.Fatal(err)
	}

	ex.drainOnce(context.Background())

	if _, done, _ := ex.Result(id); done {
		t.Fatal("should not produce result for a job claimed elsewhere")
	}
}

func TestRunComposeArgAssembly(t *testing.T) {
	ex := newExecutor(t)
	// 用假 docker 脚本回显参数，验证 composeCmd 不会重复拼进 args
	bin := t.TempDir() + "/docker"
	script := "#!/bin/sh\necho \"ARGS:$*\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ex.composeCmd = []string{bin, "compose"}

	result := ex.runCompose(context.Background(), Job{ID: "1", Filename: "web.yaml", Action: ActionUp})
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Output)
	}
	// 第一个参数必须是 compose 子命令，不能重复出现二进制名
	if !strings.HasPrefix(result.Output, "ARGS:compose -f ") {
		t.Fatalf("args should start with compose subcommand, got: %s", result.Output)
	}
	if !strings.Contains(result.Output, "--project-directory "+ex.workDir) {
		t.Fatalf("missing project-directory, got: %s", result.Output)
	}

	ex.composeCmd = []string{bin}
	result = ex.runCompose(context.Background(), Job{ID: "2", Filename: "web.yaml", Action: ActionUp})
	if !result.Success {
		t.Fatalf("standalone compose should work, got: %s", result.Output)
	}
	if !strings.HasPrefix(result.Output, "ARGS:-f ") {
		t.Fatalf("standalone compose args should start with -f, got: %s", result.Output)
	}
}

func TestPruneResultsKeepsRecent(t *testing.T) {
	ex := newExecutor(t)
	dir := filepath.Join(ex.workDir, ResultDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < resultKeep+10; i++ {
		payload := `{"id":"r` + string(rune('a'+i%26)) + strconv.Itoa(i) + `","success":true,"finishedAt":"` +
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339) + `"}`
		name := filepath.Join(dir, "r"+strconv.Itoa(i)+".json")
		if err := os.WriteFile(name, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ex.pruneResults()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != resultKeep {
		t.Fatalf("expected %d results, got %d", resultKeep, len(entries))
	}
	// 被删掉的应是最旧的
	if _, err := os.Stat(filepath.Join(dir, "r0.json")); !os.IsNotExist(err) {
		t.Fatal("oldest result should be pruned")
	}
}
