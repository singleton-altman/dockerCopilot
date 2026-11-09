package utiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
)

func TestBackupListMissingDirReturnsEmpty(t *testing.T) {
	// 全新安装：目录不存在时返回空列表而不是底层报错
	t.Setenv("BACKUP_DIR", filepath.Join(t.TempDir(), "not-exist"))
	list, err := BackupList(&svc.ServiceContext{})
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %v", list)
	}
}

func TestBackupListFiltersJsonAndYaml(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BACKUP_DIR", dir)
	for _, name := range []string{"backup-2026-11-09.json", "compose-bak.yaml", "notes.txt", "subdir"} {
		if name == "subdir" {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := BackupList(&svc.ServiceContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected json+yaml only, got %v", list)
	}
}

func TestBackupDirDefaultAndEnv(t *testing.T) {
	t.Setenv("BACKUP_DIR", "")
	if got := BackupDir(); got != "/data/backups" {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("BACKUP_DIR", "/tmp/custom-backups")
	if got := BackupDir(); got != "/tmp/custom-backups" {
		t.Fatalf("env: %q", got)
	}
}
