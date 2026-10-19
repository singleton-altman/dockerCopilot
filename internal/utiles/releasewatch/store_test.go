package releasewatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRepo(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "plain owner/repo", input: "onlyLTY/dockerCopilot", want: "onlyLTY/dockerCopilot"},
		{name: "full url", input: "https://github.com/onlyLTY/dockerCopilot", want: "onlyLTY/dockerCopilot"},
		{name: "git suffix", input: "https://github.com/onlyLTY/dockerCopilot.git", want: "onlyLTY/dockerCopilot"},
		{name: "trailing slash", input: "onlyLTY/dockerCopilot/", want: "onlyLTY/dockerCopilot"},
		{name: "trim spaces", input: "  onlyLTY/dockerCopilot  ", want: "onlyLTY/dockerCopilot"},
		{name: "empty", input: "", wantErr: true},
		{name: "missing repo", input: "onlyLTY", wantErr: true},
		{name: "too many parts", input: "a/b/c", wantErr: true},
		{name: "path traversal", input: "../etc", wantErr: true},
		{name: "dot segment", input: "a/..", wantErr: true},
		{name: "space in name", input: "a b/c", wantErr: true},
		{name: "query injection", input: "a/b?x=1", wantErr: true},
		{name: "newline", input: "a/b\nc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRepo(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeRepo(%q) expected error, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRepo(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeRepo(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStoreAddRemove(t *testing.T) {
	store := newTestStore(t)

	if err := store.Add("onlyLTY/dockerCopilot"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if repos := store.Repos(); len(repos) != 1 || repos[0] != "onlyLTY/dockerCopilot" {
		t.Fatalf("unexpected repos: %v", repos)
	}

	// 重复添加（大小写不同）应被拒绝
	if err := store.Add("onlylty/dockercopilot"); err == nil {
		t.Fatal("expected duplicate add to fail")
	}

	if err := store.Remove("onlyLTY/dockerCopilot"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if repos := store.Repos(); len(repos) != 0 {
		t.Fatalf("expected empty repos, got %v", repos)
	}

	if err := store.Remove("onlyLTY/dockerCopilot"); err == nil {
		t.Fatal("expected removing missing repo to fail")
	}
}

func TestStorePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	if err := first.Add("owner/repo"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	second := NewStore(dir)
	repos := second.Repos()
	if len(repos) != 1 || repos[0] != "owner/repo" {
		t.Fatalf("expected persisted repo, got %v", repos)
	}
}

func TestStoreRejectsInvalidRepo(t *testing.T) {
	store := newTestStore(t)
	if err := store.Add("../evil"); err == nil {
		t.Fatal("expected invalid repo to be rejected")
	}
	if _, err := os.Stat(filepath.Join(store.path)); err == nil {
		// 非法输入不应产生任何持久化文件
		if repos := store.Repos(); len(repos) != 0 {
			t.Fatalf("invalid repo was persisted: %v", repos)
		}
	}
}

func TestStoreFilePermissions(t *testing.T) {
	store := newTestStore(t)
	if err := store.Add("owner/repo"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}
