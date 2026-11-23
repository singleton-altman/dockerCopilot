package composefile

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
)

func labeledItem(id, service, state, configFiles string) types.Container {
	item := listItem(id, service, state)
	item.Labels[labelConfigFiles] = configFiles
	return item
}

func TestCollectFileStatusesGroupsByComposeFile(t *testing.T) {
	ex := newExecutor(t)
	ex.dockerAPI = &fakeSource{items: []types.Container{
		labeledItem("c1", "web", "running", "/data/stacks/emby.yml"),
		labeledItem("c2", "db", "exited", "/data/stacks/emby.yml"),
		// 多文件逗号分隔标签
		labeledItem("c3", "sub", "running", "/data/stacks/subx.yml,/data/stacks/other.yml"),
		// 无 config_files 标签的容器应忽略
		listItem("c4", "lonely", "running"),
	}}

	got := ex.CollectFileStatuses(context.Background())
	if len(got) != 3 {
		t.Fatalf("expected 3 groups, got %+v", got)
	}
	emby := got["emby.yml"]
	if len(emby.Services) != 2 || !emby.Running || emby.Healthy {
		t.Fatalf("emby status unexpected: %+v", emby)
	}
	if emby.Services[0].Name != "db" || emby.Services[1].Name != "web" {
		t.Fatalf("services not sorted: %+v", emby.Services)
	}
	sub := got["subx.yml"]
	if !sub.Running || !sub.Healthy || len(sub.Services) != 1 {
		t.Fatalf("subx status unexpected: %+v", sub)
	}
	if _, ok := got["lonely"]; ok {
		t.Fatal("container without config_files label should be ignored")
	}
}

func TestCollectFileStatusesAllRunningHealthy(t *testing.T) {
	ex := newExecutor(t)
	ex.dockerAPI = &fakeSource{items: []types.Container{
		labeledItem("c1", "web", "running", "/data/stacks/app.yaml"),
	}}
	got := ex.CollectFileStatuses(context.Background())
	app := got["app.yaml"]
	if !app.Running || !app.Healthy {
		t.Fatalf("expected running+healthy: %+v", app)
	}
}

func TestCollectFileStatusesDegrades(t *testing.T) {
	// 未注入 Docker 客户端 → 返回 nil（调用方省略状态字段）
	ex := newExecutor(t)
	if got := ex.CollectFileStatuses(context.Background()); got != nil {
		t.Fatalf("expected nil without docker source, got %+v", got)
	}
	// Docker 查询失败 → 返回 nil，不报错
	ex.dockerAPI = &fakeSource{listErr: errors.New("daemon down")}
	if got := ex.CollectFileStatuses(context.Background()); got != nil {
		t.Fatalf("expected nil on list error, got %+v", got)
	}
}

func TestFilepathBase(t *testing.T) {
	cases := map[string]string{
		"/data/stacks/emby.yml":   "emby.yml",
		"C:\\dc\\stacks\\app.yml": "app.yml",
		"simple.yml":              "simple.yml",
		"/trailing/":              "",
	}
	for in, want := range cases {
		if got := filepathBase(in); got != want {
			t.Errorf("filepathBase(%q) = %q, want %q", in, got, want)
		}
	}
}
