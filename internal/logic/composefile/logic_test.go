package composefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	store "github.com/onlyLTY/dockerCopilot/internal/utiles/composefile"
)

func newJobLogic(t *testing.T) (*Logic, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewLogic(context.Background(), &svc.ServiceContext{ComposeStore: store.NewStore(dir), ComposeExecutor: store.NewExecutor(dir)}), dir
}

func responseData(t *testing.T, response *types.Resp, code int) map[string]any {
	t.Helper()
	if response.Code != code {
		t.Fatalf("want code %d: %+v", code, response)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	t.Log(string(payload))
	return wire.Data
}

func TestJobAPIQueuedRunningFinalAndUnknownContracts(t *testing.T) {
	l, dir := newJobLogic(t)
	response, err := l.Deploy(&types.ComposeDeployReq{Filename: "web.yaml", Action: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	data := responseData(t, response, 200)
	id := data["jobID"].(string)
	if data["job"].(map[string]any)["status"] != "queued" {
		t.Fatal("submit must include queued job")
	}
	for _, state := range []string{"queued", "running", "succeeded", "failed", "interrupted"} {
		if state == "running" {
			if err := os.Rename(filepath.Join(dir, ".queue", id+".json"), filepath.Join(dir, ".queue", id+".json.claim")); err != nil {
				t.Fatal(err)
			}
		}
		final := state == "succeeded" || state == "failed" || state == "interrupted"
		if final {
			if err := os.MkdirAll(filepath.Join(dir, ".results"), 0o700); err != nil {
				t.Fatal(err)
			}
			// succeeded/failed are legacy watcher payloads (no added fields).
			result := store.JobResult{ID: id, Filename: "web.yaml", Action: store.ActionDeploy, Success: state == "succeeded", Output: "done", FinishedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
			if state == "interrupted" {
				result.Status = store.StatusInterrupted
				result.Output = "结果不确定，需核验"
			}
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".results", id+".json"), payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		response, err = l.Result(&types.ComposeJobReq{JobID: id})
		if err != nil {
			t.Fatal(err)
		}
		data = responseData(t, response, 200)
		job := data["job"].(map[string]any)
		if data["status"] != state || job["status"] != state || data["done"] != final {
			t.Fatalf("inconsistent state %s: %+v", state, data)
		}
		if _, err := time.Parse(time.RFC3339Nano, job["createdAt"].(string)); err != nil {
			t.Fatal(err)
		}
		if final {
			if _, exists := job["result"]; !exists {
				t.Fatal("final job missing result")
			}
			if data["result"].(map[string]any)["success"] != (state == "succeeded") {
				t.Fatal("false business success")
			}
		} else if _, exists := job["result"]; exists {
			t.Fatal("active job contains final result")
		}
	}
	response, _ = l.Result(&types.ComposeJobReq{JobID: "missing"})
	data = responseData(t, response, 200)
	if data["done"] != false || data["status"] != "not_found" {
		t.Fatal("unknown job reported as success")
	}
	if _, exists := data["job"]; exists {
		t.Fatal("unknown job must be omitted")
	}
	if data["result"].(map[string]any)["success"] != false {
		t.Fatal("legacy result must not imply success")
	}
}

func TestJobAPIErrorsAndReadFailureDoesNotChangeJob(t *testing.T) {
	l, dir := newJobLogic(t)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TEST_ONLY=fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, _ := l.Deploy(&types.ComposeDeployReq{Filename: ".env", Action: "deploy"})
	responseData(t, response, 400)
	response, _ = l.Result(&types.ComposeJobReq{JobID: "../unsafe"})
	responseData(t, response, 400)
	response, _ = l.Deploy(&types.ComposeDeployReq{Filename: "web.yaml", Action: "deploy"})
	id := response.Data.(map[string]any)["jobID"].(string)
	path := filepath.Join(dir, ".results", id+".json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, _ = l.Result(&types.ComposeJobReq{JobID: id})
	responseData(t, response, 400)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	response, _ = l.Result(&types.ComposeJobReq{JobID: id})
	data := responseData(t, response, 200)
	if data["status"] != "queued" || data["done"] != false {
		t.Fatal("query error mutated job")
	}
}

func TestHistoryAPISeparatesResultsAndAllActiveJobs(t *testing.T) {
	l, _ := newJobLogic(t)
	response, _ := l.History()
	data := responseData(t, response, 200)
	if len(data["results"].([]any)) != 0 || len(data["jobs"].([]any)) != 0 {
		t.Fatal("empty history must use arrays")
	}
	response, _ = l.Deploy(&types.ComposeDeployReq{Filename: "web.yaml", Action: "deploy"})
	responseData(t, response, 200)
	response, _ = l.History()
	data = responseData(t, response, 200)
	if len(data["results"].([]any)) != 0 || len(data["jobs"].([]any)) != 1 {
		t.Fatal("active job missing or included in legacy results")
	}
}

func TestDeployExposesConfigSha256AndRejectsMissingFile(t *testing.T) {
	l, dir := newJobLogic(t)
	content, err := os.ReadFile(filepath.Join(dir, "web.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])

	response, err := l.Deploy(&types.ComposeDeployReq{Filename: "web.yaml", Action: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	data := responseData(t, response, 200)
	id := data["jobID"].(string)
	// 投递响应 data.job.configSha256 与文件内容一致（hex 小写）
	if got := data["job"].(map[string]any)["configSha256"]; got != want {
		t.Fatalf("deploy response fingerprint mismatch: %v", got)
	}

	// 指纹随队列 JSON 持久化，供 watcher/任务详情核对
	raw, err := os.ReadFile(filepath.Join(dir, ".queue", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var queued store.Job
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.ConfigSha256 != want {
		t.Fatalf("queue json fingerprint mismatch: %s", raw)
	}

	// GET job 透出
	response, err = l.Result(&types.ComposeJobReq{JobID: id})
	if err != nil {
		t.Fatal(err)
	}
	data = responseData(t, response, 200)
	if got := data["job"].(map[string]any)["configSha256"]; got != want {
		t.Fatalf("job view fingerprint mismatch: %v", got)
	}

	// GET history 透出
	response, err = l.History()
	if err != nil {
		t.Fatal(err)
	}
	data = responseData(t, response, 200)
	jobs := data["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["configSha256"] != want {
		t.Fatalf("history fingerprint mismatch: %+v", jobs)
	}

	// 缺文件投递：业务错误（code!=200），不产出注定失败的任务
	queue, err := os.ReadDir(filepath.Join(dir, ".queue"))
	if err != nil {
		t.Fatal(err)
	}
	response, err = l.Deploy(&types.ComposeDeployReq{Filename: "gone.yaml", Action: "up"})
	if err != nil {
		t.Fatal(err)
	}
	responseData(t, response, 400)
	after, err := os.ReadDir(filepath.Join(dir, ".queue"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(queue) {
		t.Fatalf("rejected deploy must not enqueue a job: %d -> %d", len(queue), len(after))
	}
}

func TestJobResultWithServicesAndShaSurvivesAPI(t *testing.T) {
	l, dir := newJobLogic(t)
	response, err := l.Deploy(&types.ComposeDeployReq{Filename: "web.yaml", Action: "up"})
	if err != nil {
		t.Fatal(err)
	}
	id := response.Data.(map[string]any)["jobID"].(string)
	// 模拟新版 watcher 回写：终态结果带指纹与服务状态
	final := store.JobResult{ID: id, Filename: "web.yaml", Action: store.ActionUp, Success: true,
		Output: "done", FinishedAt: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC),
		ConfigSha256: "abc123",
		Services: []store.ServiceState{
			{Name: "web", State: "running"},
			{Name: "db", State: "running", Health: "healthy"},
		}}
	payload, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".results"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".results", id+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	response, err = l.Result(&types.ComposeJobReq{JobID: id})
	if err != nil {
		t.Fatal(err)
	}
	data := responseData(t, response, 200)
	result := data["result"].(map[string]any)
	if result["configSha256"] != "abc123" {
		t.Fatalf("result fingerprint mismatch: %+v", result)
	}
	services, ok := result["services"].([]any)
	if !ok || len(services) != 2 {
		t.Fatalf("services missing: %+v", result)
	}
	db := services[1].(map[string]any)
	if db["health"] != "healthy" {
		t.Fatalf("health mismatch: %v", db)
	}
	job := data["job"].(map[string]any)
	if job["configSha256"] != "abc123" {
		t.Fatalf("job view fingerprint mismatch: %+v", job)
	}
}
