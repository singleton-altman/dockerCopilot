package logoshare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const embedJSON = `{
  "version": "embedded",
  "mappings": {"nginx": "nginx.svg", "mysql": "mysql.png"},
  "mono": ["nginx.svg"]
}`

func TestMergedWithoutRemoteReturnsEmbed(t *testing.T) {
	s := New("")
	embedMappings := map[string]string{"nginx": "nginx.svg"}
	embedMono := []string{"nginx.svg"}

	mappings, gotMono, gotVersion := s.Merged(embedMappings, embedMono, "")
	if mappings["nginx"] != "nginx.svg" {
		t.Fatalf("mappings: %+v", mappings)
	}
	if len(gotMono) != 1 || gotMono[0] != "nginx.svg" {
		t.Fatalf("mono: %+v", gotMono)
	}
	if gotVersion != "embedded" {
		t.Fatalf("version: %q", gotVersion)
	}
	if s.Enabled() {
		t.Fatal("empty url must be disabled")
	}
}

func TestRemoteOverridesAndMerges(t *testing.T) {
	var remote = `{
  "version": "2026-10-26.44",
  "mappings": {
    "nginx": "nginx.svg",
    "traefik": "traefikproxy.svg",
    "gitea": "gitea.svg"
  },
  "mono": ["traefikproxy.svg", "gitea.svg"]
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(remote))
	}))
	defer srv.Close()

	s := New(srv.URL)
	if !s.Enabled() {
		t.Fatal("must be enabled")
	}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	mappings, mono, version := s.Merged(
		map[string]string{"nginx": "nginx.svg", "mysql": "mysql.png"},
		[]string{"nginx.svg"},
		"https://cdn.example.com/manager/",
	)

	// 同名键远程覆盖：命中远程包的同名文件，走远程绝对 URL
	if mappings["nginx"] != "https://cdn.example.com/manager/share-icon-nginx.svg" {
		t.Fatalf("nginx: %q", mappings["nginx"])
	}
	// 仅内置的键保留内置相对路径
	if mappings["mysql"] != "mysql.png" {
		t.Fatalf("mysql: %q", mappings["mysql"])
	}
	// 仅远程的键进入合并结果
	if mappings["traefik"] != "https://cdn.example.com/manager/share-icon-traefikproxy.svg" {
		t.Fatalf("traefik: %q", mappings["traefik"])
	}

	// mono 并集
	hasTraefik, hasGitea, hasNginx := false, false, false
	for _, m := range mono {
		switch m {
		case "traefikproxy.svg":
			hasTraefik = true
		case "gitea.svg":
			hasGitea = true
		case "nginx.svg":
			hasNginx = true
		}
	}
	if !hasTraefik || !hasGitea || !hasNginx {
		t.Fatalf("mono union: %+v", mono)
	}
	if version != "2026-10-26.44" {
		t.Fatalf("version: %q", version)
	}
}

func TestRefreshRejectsInvalidPack(t *testing.T) {
	for name, body := range map[string]string{
		"missing version":   `{"mappings":{"a":"a.svg"}}`,
		"missing mappings":  `{"version":"v1"}`,
		"path in file name": `{"version":"v1","mappings":{"a":"../x.svg"}}`,
		"empty key":         `{"version":"v1","mappings":{"":"a.svg"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		s := New(srv.URL)
		if err := s.Refresh(context.Background()); err == nil {
			t.Errorf("%s: expected error", name)
		}
		srv.Close()
	}
}

func TestRefreshFailureKeepsLastGoodPack(t *testing.T) {
	good := `{"version":"v1","mappings":{"nginx":"nginx.svg"},"mono":["nginx.svg"]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(good))
	}))
	defer srv.Close()

	s := New(srv.URL)
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 服务端开始返回坏数据：上一次成功结果必须保留
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if err := s.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	s.mu.RLock()
	_, hasRemote := s.remote.Mappings["nginx"]
	s.mu.RUnlock()
	if !hasRemote {
		t.Fatal("last good pack must be preserved")
	}
}

var _ = json.Marshal
