package composefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubmitWithConfigWritesShaToQueueJSON(t *testing.T) {
	ex := newExecutor(t)
	const sha = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	id, err := ex.SubmitWithConfig("web.yaml", ActionDeploy, sha)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(ex.queueDir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job.ConfigSha256 != sha {
		t.Fatalf("queue json must carry the fingerprint: %s", raw)
	}

	// 不带指纹的投递（旧入口）不产出该字段，保持旧队列 JSON 形状
	id2, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(filepath.Join(ex.queueDir, id2+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if containsJSONKey(raw2, "configSha256") {
		t.Fatalf("plain submit must omit configSha256: %s", raw2)
	}
}

func TestClaimWarnsWhenFileDriftedAfterSubmit(t *testing.T) {
	ex := newExecutor(t)
	defer releaseOwner(t, ex)
	if err := os.WriteFile(filepath.Join(ex.workDir, "web.yaml"), []byte("services:\n  web: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := "1111111111111111111111111111111111111111111111111111111111111111"
	id, err := ex.SubmitWithConfig("web.yaml", ActionUp, stale)
	if err != nil {
		t.Fatal(err)
	}
	ex.runner = func(context.Context, Job) JobResult { return JobResult{Success: true, Output: "ran"} }

	ex.claimAndRun(context.Background(), id)

	result, done, err := ex.Result(id)
	if err != nil || !done || !result.Success {
		t.Fatalf("drift must not fail the job: done=%v err=%v result=%+v", done, err, result)
	}
	found := false
	for _, step := range result.Steps {
		if step.Stage == "fingerprint" {
			found = true
			if step.Status != "warning" || !strings.Contains(step.Message, "投递后") {
				t.Fatalf("unexpected drift step: %+v", step)
			}
		}
	}
	if !found {
		t.Fatalf("drifted file must add a warning step: %+v", result.Steps)
	}

	// 未漂移时不得出现该步骤
	current, err := os.ReadFile(filepath.Join(ex.workDir, "web.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(current)
	id2, err := ex.SubmitWithConfig("web.yaml", ActionUp, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	ex.claimAndRun(context.Background(), id2)
	clean, done, err := ex.Result(id2)
	if err != nil || !done {
		t.Fatal(err)
	}
	for _, step := range clean.Steps {
		if step.Stage == "fingerprint" {
			t.Fatalf("unchanged file must not add drift step: %+v", clean.Steps)
		}
	}
}

func containsJSONKey(payload []byte, key string) bool {
	var wire map[string]any
	if json.Unmarshal(payload, &wire) != nil {
		return false
	}
	_, ok := wire[key]
	return ok
}

func TestClaimPropagatesFingerprintToResultAndView(t *testing.T) {
	ex := newExecutor(t)
	ex.runner = func(ctx context.Context, job Job) JobResult {
		return JobResult{Success: true, Output: "ran"}
	}
	defer releaseOwner(t, ex)
	const sha = "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe"

	id, err := ex.SubmitWithConfig("web.yaml", ActionDeploy, sha)
	if err != nil {
		t.Fatal(err)
	}
	// 队列中的任务视图应先带上指纹
	view := requireView(t, ex, id, StatusQueued)
	if view.ConfigSha256 != sha {
		t.Fatalf("queued view sha=%q", view.ConfigSha256)
	}

	ex.claimAndRun(context.Background(), id)

	result, done, err := ex.Result(id)
	if err != nil || !done {
		t.Fatalf("expected finished result, done=%v err=%v", done, err)
	}
	if result.ConfigSha256 != sha {
		t.Fatalf("final result sha=%q", result.ConfigSha256)
	}
	view = requireView(t, ex, id, StatusSucceeded)
	if view.ConfigSha256 != sha || view.Result == nil || view.Result.ConfigSha256 != sha {
		t.Fatalf("final view must expose fingerprint: %+v", view)
	}
}

func TestViewFallsBackToQueueFingerprintForLegacyResult(t *testing.T) {
	ex := newExecutor(t)
	const sha = "feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface"
	id, err := ex.SubmitWithConfig("web.yaml", ActionUp, sha)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧 watcher：认领了带指纹的任务，但回写结果没有新字段
	if err := os.Rename(filepath.Join(ex.queueDir, id+".json"), filepath.Join(ex.queueDir, id+".json.claim")); err != nil {
		t.Fatal(err)
	}
	legacy := JobResult{ID: id, Filename: "web.yaml", Action: ActionUp, Success: true,
		FinishedAt: time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)}
	if err := os.MkdirAll(ex.resultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ex.resultDir, id+".json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	view := requireView(t, ex, id, StatusSucceeded)
	if view.ConfigSha256 != sha {
		t.Fatalf("view must recover fingerprint from queue job: %+v", view)
	}
	if view.Result.ConfigSha256 != "" {
		t.Fatalf("legacy result itself must stay untouched: %+v", view.Result)
	}
}
