package composefile

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHostPathForDir(t *testing.T) {
	mounts := []MountView{
		{Type: "volume", Source: "myvol", Destination: "/data"},
		{Type: "bind", Source: "/vol1/docker/dc/data", Destination: "/data"},
		{Type: "bind", Source: "/vol1/docker/dc/data/dc", Destination: "/data/dc"},
	}

	if got := HostPathForDir(mounts, "/data/stacks"); got != "/vol1/docker/dc/data/stacks" {
		t.Fatalf("prefix mount: got %q", got)
	}
	// 更长前缀优先
	if got := HostPathForDir(mounts, "/data/dc/stacks"); got != "/vol1/docker/dc/data/dc/stacks" {
		t.Fatalf("longest prefix: got %q", got)
	}
	if got := HostPathForDir(mounts, "/other"); got != "" {
		t.Fatalf("unmatched dir should be empty, got %q", got)
	}
	if got := HostPathForDir(nil, "/data/stacks"); got != "" {
		t.Fatalf("no mounts: got %q", got)
	}
	if got := HostPathForDir(mounts, "relative"); got != "" {
		t.Fatalf("relative dir: got %q", got)
	}
}

const rewriteInput = `# 主配置
services:
  web:
    image: nginx
    env_file:
      - ./db.env        # 相对 project-directory，不应被改写
    volumes:
      - ./config:/config
      - ../shared:/shared:ro
      - data:/data
      - /abs/path:/abs
      - .:/base
      - type: volume
        source: myvol
        target: /v
      - source: ./logs
        target: /logs
`

func TestRewriteRelativeVolumes(t *testing.T) {
	out, changed, err := RewriteRelativeVolumes(rewriteInput, "/vol1/dc/stacks")
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if !changed {
		t.Fatal("expected changed")
	}
	for _, want := range []string{
		"/vol1/dc/stacks/config:/config",
		"/vol1/dc/shared:/shared:ro",
		"/vol1/dc/stacks:/base",
		"source: /vol1/dc/stacks/logs",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// 不应改写的内容：命名卷、绝对路径、type: volume 保持原样
	for _, keep := range []string{"data:/data", "/abs/path:/abs", "type: volume", "source: myvol"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lost %q in:\n%s", keep, out)
		}
	}
	if !strings.Contains(out, "./db.env") {
		t.Fatalf("env_file should keep relative:\n%s", out)
	}
	if !strings.Contains(out, "# 主配置") || !strings.Contains(out, "# 相对 project-directory，不应被改写") {
		t.Fatalf("comments lost:\n%s", out)
	}
}

func TestRewriteRelativeVolumesNoop(t *testing.T) {
	doc := "services:\n  web:\n    image: nginx\n    volumes:\n      - data:/data\n"
	out, changed, err := RewriteRelativeVolumes(doc, "/host")
	if err != nil || changed || out != doc {
		t.Fatalf("expected noop, changed=%v err=%v out=%q", changed, err, out)
	}

	// 空 hostDir 直接原样返回
	out, changed, err = RewriteRelativeVolumes(rewriteInput, "")
	if err != nil || changed || out != rewriteInput {
		t.Fatalf("expected noop with empty hostDir, changed=%v err=%v", changed, err)
	}
}

func TestRewriteRelativeVolumesInvalidYAML(t *testing.T) {
	if _, _, err := RewriteRelativeVolumes("services: [unclosed", "/host"); err == nil {
		t.Fatal("expected yaml error")
	}
}

func TestRewritePathSource(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"./config", filepath.Join("/host", "config"), true},
		{".", "/host", true},
		{"../shared", filepath.Clean("/host/../shared"), true},
		{"/abs", "", false},
		{"myvol", "", false},
		{"~/x", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := rewritePathSource(tc.in, "/host")
		if ok != tc.ok || got != tc.want {
			t.Errorf("rewritePathSource(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
