package releasewatch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestChecker(t *testing.T, handler http.HandlerFunc) (*Checker, *Store) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	store := NewStore(t.TempDir())
	checker := NewChecker(store, "")
	checker.baseURL = server.URL
	return checker, store
}

func TestLatestReleaseParsesPayload(t *testing.T) {
	checker, _ := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tag_name": "v2.1.4",
			"name": "v2.1.4",
			"html_url": "https://github.com/owner/repo/releases/tag/v2.1.4",
			"published_at": "2026-10-19T10:00:00Z",
			"prerelease": false,
			"draft": false
		}`))
	})

	release, err := checker.LatestRelease("owner/repo")
	if err != nil {
		t.Fatalf("LatestRelease failed: %v", err)
	}
	if release.Tag != "v2.1.4" {
		t.Fatalf("unexpected tag: %s", release.Tag)
	}
	if release.URL == "" {
		t.Fatal("expected release url")
	}
}

func TestLatestReleaseErrorStatuses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "not found", statusCode: http.StatusNotFound},
		{name: "rate limited", statusCode: http.StatusTooManyRequests},
		{name: "server error", statusCode: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker, _ := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			})
			if _, err := checker.LatestRelease("owner/repo"); err == nil {
				t.Fatalf("expected error for status %d", tt.statusCode)
			}
		})
	}
}

func TestCheckRepoBaselineThenNew(t *testing.T) {
	tag := "v1.0.0"
	checker, store := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","name":"` + tag + `","html_url":"https://example.invalid","published_at":"2026-01-01T00:00:00Z"}`))
	})

	if err := store.Add("owner/repo"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// 第一次检查只建立基线
	first := checker.CheckRepo("owner/repo")
	if first.Error != "" {
		t.Fatalf("unexpected error: %s", first.Error)
	}
	if first.IsNew {
		t.Fatal("first check must not be treated as new")
	}
	if state := store.State("owner/repo"); state.LastTag != "v1.0.0" {
		t.Fatalf("expected baseline v1.0.0, got %q", state.LastTag)
	}

	// 相同版本再次检查，仍不是新版本
	if again := checker.CheckRepo("owner/repo"); again.IsNew {
		t.Fatal("same tag must not be treated as new")
	}

	// 版本变化后应标记为新版本
	tag = "v1.0.1"
	result := checker.CheckRepo("owner/repo")
	if !result.IsNew {
		t.Fatal("expected new tag to be detected")
	}
	if result.Release.Tag != "v1.0.1" {
		t.Fatalf("unexpected tag: %s", result.Release.Tag)
	}
}

func TestCheckRepoRecordsError(t *testing.T) {
	checker, store := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := store.Add("owner/repo"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	result := checker.CheckRepo("owner/repo")
	if result.Error == "" {
		t.Fatal("expected error to be recorded")
	}
	if state := store.State("owner/repo"); state.LastError == "" {
		t.Fatal("expected lastError to be persisted")
	}
}
