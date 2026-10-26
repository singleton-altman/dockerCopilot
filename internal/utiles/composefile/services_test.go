package composefile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// fakeSource 用假数据模拟 docker 只读接口，验证 services 构造与 ps 结论，
// 不触碰真实 Docker，也不走 CLI 假脚本以外的进程。
type fakeSource struct {
	items     []types.Container
	listErr   error
	states    map[string]types.ContainerJSON
	lastList  container.ListOptions
	inspected []string
}

func (f *fakeSource) ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
	f.lastList = options
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.items, nil
}

func (f *fakeSource) ContainerInspect(ctx context.Context, id string) (types.ContainerJSON, error) {
	f.inspected = append(f.inspected, id)
	if state, ok := f.states[id]; ok {
		return state, nil
	}
	return types.ContainerJSON{}, fmt.Errorf("no such container: %s", id)
}

func listItem(id, service, state string) types.Container {
	return types.Container{
		ID: id, Names: []string{"/" + id}, State: state,
		Labels: map[string]string{labelService: service},
	}
}

func inspectState(status, health string) types.ContainerJSON {
	state := &types.ContainerState{Status: status}
	if health != "" {
		state.Health = &types.Health{Status: health}
	}
	return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{State: state}}
}

func TestSummarizeServiceStatesBranches(t *testing.T) {
	cases := []struct {
		name     string
		services []ServiceState
		warning  string
		status   string
		contains string
		nilOut   bool
	}{
		{"all running", []ServiceState{{Name: "web", State: "running"}}, "", "success", "全部运行中", false},
		{"health passes", []ServiceState{{Name: "web", State: "running", Health: "healthy"}}, "", "success", "健康检查通过", false},
		{"health starting is not a failure", []ServiceState{{Name: "web", State: "running", Health: "starting"}}, "", "success", "全部运行中", false},
		{"exited is warning", []ServiceState{{Name: "web", State: "running"}, {Name: "db", State: "exited"}}, "", "warning", "exited", false},
		{"unhealthy is warning", []ServiceState{{Name: "web", State: "running", Health: "unhealthy"}}, "", "warning", "unhealthy", false},
		{"no containers", []ServiceState{}, "", "success", "没有发现", false},
		{"enumeration failed", nil, "查询本文件容器失败: boom", "warning", "查询本文件容器失败", true},
	}
	for _, tc := range cases {
		step, out := summarizeServiceStates(tc.services, tc.warning)
		if step.Stage != "ps" || step.Status != tc.status {
			t.Errorf("%s: step=%+v", tc.name, step)
		}
		if !strings.Contains(step.Message, tc.contains) {
			t.Errorf("%s: message=%q", tc.name, step.Message)
		}
		if tc.nilOut && out != nil {
			t.Errorf("%s: failed enumeration must not publish services", tc.name)
		}
		if !tc.nilOut && tc.warning == "" && len(out) != len(tc.services) {
			t.Errorf("%s: services round-trip mismatch", tc.name)
		}
	}
}

func TestCollectOwnServicesFiltersByConfigFilesAndInspects(t *testing.T) {
	ex := newExecutor(t)
	source := &fakeSource{
		items: []types.Container{
			listItem("c2", "db", "running"),
			listItem("c1", "web", "running"),
			{ID: "c3", Names: []string{"/bare"}, State: "running", Labels: map[string]string{}},
		},
		states: map[string]types.ContainerJSON{
			"c1": inspectState("running", "healthy"),
		},
	}
	services, warning := ex.collectOwnServices(context.Background(), source, "web.yaml")
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	// 过滤必须发生在 daemon 侧：按本文件绝对路径的 label 精确筛选，而不是全量 list。
	labels := source.lastList.Filters.Get("label")
	want := labelConfigFiles + "=" + ex.workDir + "/web.yaml"
	if len(labels) != 1 || labels[0] != want {
		t.Fatalf("label filter mismatch: %v", labels)
	}
	if !source.lastList.All {
		t.Fatal("must include stopped containers")
	}
	// 只 inspect 本文件的 3 个容器
	if len(source.inspected) != 3 {
		t.Fatalf("inspected %v", source.inspected)
	}
	if len(services) != 3 {
		t.Fatalf("services=%+v", services)
	}
	// 输出按服务名稳定排序；health 只在有 healthcheck 时存在
	if services[0].Name != "bare" || services[1].Name != "db" || services[2].Name != "web" {
		t.Fatalf("order/name mismatch: %+v", services)
	}
	if services[2].Health != "healthy" || services[1].Health != "" {
		t.Fatalf("health mismatch: %+v", services)
	}
}

func TestCollectOwnServicesDegradesOnErrors(t *testing.T) {
	ex := newExecutor(t)
	source := &fakeSource{listErr: fmt.Errorf("daemon down")}
	services, warning := ex.collectOwnServices(context.Background(), source, "web.yaml")
	if services != nil || warning == "" {
		t.Fatalf("list failure must yield warning: %+v %q", services, warning)
	}

	// 单个 inspect 失败退回 list 的状态，不伪造健康结论，也不算枚举失败
	source = &fakeSource{items: []types.Container{listItem("c1", "web", "running")}}
	services, warning = ex.collectOwnServices(context.Background(), source, "web.yaml")
	if warning != "" {
		t.Fatalf("inspect failure must not warn: %s", warning)
	}
	if len(services) != 1 || services[0].State != "running" || services[0].Health != "" {
		t.Fatalf("fallback state mismatch: %+v", services)
	}
}

func TestAttachServiceSnapshotNilClientWarnsButKeepsSuccess(t *testing.T) {
	ex := newExecutor(t)
	job := Job{ID: "1", Filename: "web.yaml", Action: ActionUp}
	result := JobResult{Success: true, Status: StatusSucceeded}
	got := ex.attachServiceSnapshot(context.Background(), job, result)
	if !got.Success || got.Status != StatusSucceeded {
		t.Fatalf("warning must not overturn success: %+v", got)
	}
	if got.Services != nil {
		t.Fatalf("nil client must not produce services: %+v", got.Services)
	}
	if len(got.Steps) != 1 || got.Steps[0].Stage != "ps" || got.Steps[0].Status != "warning" ||
		!strings.Contains(got.Steps[0].Message, "未注入 Docker 客户端") {
		t.Fatalf("expected explaining warning step: %+v", got.Steps)
	}
}

// fakeComposeCLI 返回一个记录调用并可按子命令定制的假 docker 脚本。
func fakeComposeCLI(t *testing.T, body string) []string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\n" + body + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{bin, "compose"}
}

func TestRunJobProducesServicesForUpDeployRestartOnly(t *testing.T) {
	healthy := func() *fakeSource {
		return &fakeSource{
			items: []types.Container{listItem("c1", "web", "running")},
			states: map[string]types.ContainerJSON{
				"c1": inspectState("running", "healthy"),
			},
		}
	}
	cases := []struct {
		name    string
		action  Action
		body    string
		wantPS  bool
		wantSvc bool
	}{
		{"up", ActionUp, `case "$*" in *up*) echo " Container web Started ";; esac`, true, true},
		{"restart", ActionRestart, "", true, true},
		{"deploy", ActionDeploy, `case "$*" in *pull*) echo " Image web:1 Pulled ";; *up*) echo " Container web Recreated ";; esac`, true, true},
		{"down", ActionDown, "", false, false},
		{"pull", ActionPull, `echo " Image web:1 Pulled "`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := newExecutor(t)
			ex.composeCmd = fakeComposeCLI(t, tc.body)
			source := healthy()
			ex.dockerAPI = source

			result := ex.runJob(context.Background(), Job{ID: "1", Filename: "web.yaml", Action: tc.action})
			if !result.Success {
				t.Fatalf("expected success: %s", result.Output)
			}
			hasPS, hasServices := false, result.Services != nil
			for _, step := range result.Steps {
				if step.Stage == "ps" {
					hasPS = true
					if tc.wantSvc && step.Status != "success" {
						t.Fatalf("ps should succeed: %+v", step)
					}
				}
			}
			if hasPS != tc.wantPS || hasServices != tc.wantSvc {
				t.Fatalf("ps=%v services=%v, want %v/%v: %+v", hasPS, hasServices, tc.wantPS, tc.wantSvc, result)
			}
			if tc.wantSvc {
				if len(result.Services) != 1 || result.Services[0].Name != "web" ||
					result.Services[0].State != "running" || result.Services[0].Health != "healthy" {
					t.Fatalf("unexpected services: %+v", result.Services)
				}
				if len(source.inspected) != 1 || source.inspected[0] != "c1" {
					t.Fatalf("must inspect only this file's containers: %v", source.inspected)
				}
			}
		})
	}
}

func TestRunJobFailedUpProducesNoServices(t *testing.T) {
	ex := newExecutor(t)
	ex.composeCmd = fakeComposeCLI(t, `exit 1`)
	ex.dockerAPI = &fakeSource{items: []types.Container{listItem("c1", "web", "exited")}}

	result := ex.runJob(context.Background(), Job{ID: "1", Filename: "web.yaml", Action: ActionUp})
	if result.Success || result.Services != nil {
		t.Fatalf("failed up must not publish services: %+v", result)
	}
	for _, step := range result.Steps {
		if step.Stage == "ps" {
			t.Fatal("failed up must not append ps step")
		}
	}
}

func TestRunJobInterruptedDeployProducesNoServices(t *testing.T) {
	ex := newExecutor(t)
	job := Job{ID: "1", Filename: "web.yaml", Action: ActionDeploy}
	for _, result := range []JobResult{
		{Success: false, Status: StatusSucceeded},
		{Success: true, Status: StatusInterrupted},
		{Success: false, Status: StatusInterrupted},
	} {
		if ex.serviceSnapshotApplies(job, result) {
			t.Fatalf("interrupted/failed job must not collect services: %+v", result)
		}
		got := ex.attachServiceSnapshot(context.Background(), job, result)
		if got.Services != nil || len(got.Steps) != 0 {
			t.Fatalf("interrupted/failed job must stay untouched: %+v", got)
		}
	}
	// down/pull 即便成功也不产出
	for _, action := range []Action{ActionDown, ActionPull} {
		if ex.serviceSnapshotApplies(Job{Action: action}, JobResult{Success: true, Status: StatusSucceeded}) {
			t.Fatalf("%s must not collect services", action)
		}
	}
}

func TestServiceResultJSONShape(t *testing.T) {
	result := JobResult{
		ID: "1", Filename: "web.yaml", Action: ActionDeploy, Success: true, Status: StatusSucceeded,
		FinishedAt:   time.Now(),
		ConfigSha256: "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b",
		Services: []ServiceState{
			{Name: "web", State: "running"},
			{Name: "db", State: "running", Health: "healthy"},
		},
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	services, ok := wire["services"].([]any)
	if !ok || len(services) != 2 {
		t.Fatalf("services shape: %s", payload)
	}
	web := services[0].(map[string]any)
	if web["name"] != "web" || web["state"] != "running" {
		t.Fatalf("web entry: %v", web)
	}
	if _, exists := web["health"]; exists {
		t.Fatal("health must be omitted without healthcheck")
	}
	db := services[1].(map[string]any)
	if db["health"] != "healthy" {
		t.Fatalf("db entry: %v", db)
	}
	if wire["configSha256"] != result.ConfigSha256 {
		t.Fatalf("configSha256 missing: %s", payload)
	}

	// 空值全部省略：旧客户端拿到的 JSON 与旧版本形状一致
	empty, err := json.Marshal(JobResult{ID: "2", Success: true, FinishedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"services", "configSha256", "steps", "status"} {
		if strings.Contains(string(empty), `"`+key+`"`) {
			t.Fatalf("empty result must omit %s: %s", key, empty)
		}
	}
}

func TestLegacyJSONWithoutNewFieldsDecodes(t *testing.T) {
	base := `{"id":"a","filename":"web.yaml","action":"up","success":true,"output":"done","finishedAt":"2026-10-19T10:00:00Z"}`
	var result JobResult
	if err := json.Unmarshal([]byte(base), &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigSha256 != "" || result.Services != nil {
		t.Fatalf("legacy fields must stay empty: %+v", result)
	}

	var job Job
	if err := json.Unmarshal([]byte(`{"id":"a","filename":"web.yaml","action":"up","createdAt":"2026-10-19T10:00:00Z"}`), &job); err != nil {
		t.Fatal(err)
	}
	if job.ConfigSha256 != "" {
		t.Fatalf("legacy job sha must be empty: %+v", job)
	}

	var view JobView
	if err := json.Unmarshal([]byte(`{"id":"a","filename":"web.yaml","action":"up","status":"queued","createdAt":"2026-10-19T10:00:00Z"}`), &view); err != nil {
		t.Fatal(err)
	}
	if view.ConfigSha256 != "" {
		t.Fatalf("legacy view sha must be empty: %+v", view)
	}
}
