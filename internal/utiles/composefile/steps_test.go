package composefile

import (
	"strings"
	"testing"
)

func TestParsePullOutputMixed(t *testing.T) {
	output := ` Image dc_log_local:v1 Pulling 
 Image dc_log_built:latest Pulling 
 Image nginx:alpine Pulling 
 Image dc_log_built:latest pull access denied for dc_log_built, repository does not exist or may require 'docker login' 
 Image dc_log_local:v1 Error pull access denied for dc_log_local, repository does not exist or may require 'docker login'
 Image nginx:alpine Pulled 
time="2026-10-23T11:01:14+08:00" level=warning msg="WARNING: Some service image(s) must be built from source"`
	outcomes := parsePullOutput(output)
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 images, got %d: %+v", len(outcomes), outcomes)
	}
	byImage := map[string]string{}
	for _, item := range outcomes {
		byImage[item.Image] = item.Status
	}
	if byImage["nginx:alpine"] != "pulled" {
		t.Errorf("nginx should be pulled, got %q", byImage["nginx:alpine"])
	}
	if byImage["dc_log_local:v1"] != "failed" {
		t.Errorf("local image should fail, got %q", byImage["dc_log_local:v1"])
	}
	if byImage["dc_log_built:latest"] != "failed" {
		t.Errorf("build-only image should fail, got %q", byImage["dc_log_built:latest"])
	}
}

// 真实 compose 在失败时会额外输出一行不带 "Image " 前缀的 Error 行，
// 其中的 "Error" 不能被当成镜像名。
func TestParsePullOutputIgnoresBareErrorLine(t *testing.T) {
	output := ` Image a:1 Pulling 
 Image a:1 Error pull access denied for a, repository does not exist
Error response from daemon: pull access denied for a
`
	outcomes := parsePullOutput(output)
	if len(outcomes) != 1 {
		t.Fatalf("expected only one image, got %+v", outcomes)
	}
	if outcomes[0].Image != "a:1" || outcomes[0].Status != "failed" {
		t.Fatalf("unexpected outcome: %+v", outcomes[0])
	}
}

func TestSummarizePullAllSuccess(t *testing.T) {
	output := " Image a:1 Pulling \n Image a:1 Pulled \n Image b:2 Pulled \n"
	step, _ := summarizePull(output)
	if step.Status != "success" {
		t.Fatalf("expected success, got %+v", step)
	}
	if !strings.Contains(step.Message, "已拉取 2 个镜像") {
		t.Errorf("unexpected message: %s", step.Message)
	}
}

func TestSummarizePullPartialFailure(t *testing.T) {
	output := " Image a:1 Pulled \n Image b:2 Error pull access denied\n"
	step, _ := summarizePull(output)
	if step.Status != "warning" {
		t.Fatalf("expected warning, got %+v", step)
	}
	if !strings.Contains(step.Message, "1 个拉取失败") {
		t.Errorf("message should report failure count, got: %s", step.Message)
	}
	// 「改用本地镜像」是 deploy 上下文的补充说明，不在 summarizePull 里断言
	if strings.Contains(step.Message, "本地已有镜像") {
		t.Errorf("summarizePull should not assume fallback, got: %s", step.Message)
	}
}

func TestSummarizePullSkipped(t *testing.T) {
	step, _ := summarizePull(" Image a:1 Skipped \n")
	if step.Status != "success" || !strings.Contains(step.Message, "跳过") {
		t.Fatalf("expected skipped summary, got %+v", step)
	}
}

func TestSummarizeUpRecreate(t *testing.T) {
	output := ` Container web Recreate 
 Container web Recreated 
 Container web Starting 
 Container web Started 
`
	step := summarizeUp(output)
	if step.Status != "success" {
		t.Fatalf("expected success, got %+v", step)
	}
	if !strings.Contains(step.Message, "已重建") {
		t.Errorf("should mention recreate, got: %s", step.Message)
	}
}

func TestSummarizeUpUnchanged(t *testing.T) {
	step := summarizeUp(" Container web Running \n")
	if !strings.Contains(step.Message, "无变化") {
		t.Fatalf("expected unchanged, got: %s", step.Message)
	}
	if !isAllUnchanged(step.Message) {
		t.Error("isAllUnchanged should be true for a single running container")
	}
}

func TestSummarizeUpMixed(t *testing.T) {
	output := " Container web Running \n Container db Started \n Container old Removed \n"
	step := summarizeUp(output)
	if !strings.Contains(step.Message, "无变化") || !strings.Contains(step.Message, "已启动") ||
		!strings.Contains(step.Message, "已移除") {
		t.Fatalf("unexpected mixed summary: %s", step.Message)
	}
	if isAllUnchanged(step.Message) {
		t.Error("mixed result should not be treated as all-unchanged")
	}
}

func TestSummarizeSinglePullMarksFailure(t *testing.T) {
	steps := summarizeSingle(ActionPull, " Image a:1 Error boom\n", false)
	if len(steps) != 1 || steps[0].Status != "failed" {
		t.Fatalf("expected failed pull step, got %+v", steps)
	}
}

func TestAllPulled(t *testing.T) {
	if allPulled([]imagePullOutcome{{Status: "pulled"}}) != true {
		t.Error("single pulled image should be allPulled")
	}
	if allPulled([]imagePullOutcome{{Status: "pulled"}, {Status: "failed"}}) {
		t.Error("mixed outcomes must not be allPulled")
	}
	if allPulled(nil) {
		t.Error("empty outcomes must not be allPulled")
	}
}
