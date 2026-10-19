package composefile

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

const validCompose = `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
`

func TestSaveReadDelete(t *testing.T) {
	store := newStore(t)

	if _, err := store.Save("app.yaml", validCompose); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	content, err := store.Read("app.yaml")
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if content != validCompose {
		t.Fatalf("content mismatch: %q", content)
	}

	items, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 1 || items[0].Name != "app.yaml" {
		t.Fatalf("unexpected list: %+v", items)
	}

	if err := store.Delete("app.yaml"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Read("app.yaml"); err == nil {
		t.Fatal("expected read after delete to fail")
	}
}

func TestListMissingDirReturnsEmpty(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "not-created"))
	items, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty list, got %+v", items)
	}
}

func TestRejectsUnsafeNames(t *testing.T) {
	store := newStore(t)
	names := []string{
		"",
		"../evil.yaml",
		"/etc/passwd.yaml",
		"sub/app.yaml",
		"app.txt",
		"app.yaml.tmp",
		".hidden.yaml",
		"app yaml.yaml",
		"app;rm.yaml",
	}
	for _, name := range names {
		if _, err := store.Save(name, validCompose); err == nil {
			t.Errorf("expected Save(%q) to fail", name)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid", content: validCompose},
		{name: "valid yml no ports", content: "services:\n  db:\n    image: postgres:16\n"},
		{name: "empty", content: "", wantErr: true},
		{name: "not yaml", content: "this is not: [valid", wantErr: true},
		{name: "missing services", content: "version: '3'\n", wantErr: true},
		{name: "empty services", content: "services: {}\n", wantErr: true},
		{name: "services wrong type", content: "services: hello\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.content)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q", tt.content)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSaveRejectsInvalidCompose(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("bad.yaml", "services: {}\n"); err == nil {
		t.Fatal("expected invalid compose to be rejected")
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "bad.yaml")); err == nil {
		t.Fatal("invalid content should not be written")
	}
}

func TestAtomicWrite(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("web.yml", validCompose); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	// 不应残留临时文件
	if _, err := os.Stat(filepath.Join(store.Dir(), "web.yml.tmp")); err == nil {
		t.Fatal("temporary file was not cleaned up")
	}
}
