package composefile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const v1 = "services:\n  a:\n    image: alpine:3.20\n"
const v2 = "services:\n  a:\n    image: alpine:3.21\n"

// 首次保存不产生备份，覆盖保存才产生。
func TestSaveCreatesBackupOnOverwrite(t *testing.T) {
	store := newStore(t)

	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	backups := listDir(t, filepath.Join(store.Dir(), backupDirName))
	if len(backups) != 0 {
		t.Fatalf("first save should not create backup, got %v", backups)
	}

	if _, err := store.Save("app.yaml", v2); err != nil {
		t.Fatalf("second save failed: %v", err)
	}
	backups = listDir(t, filepath.Join(store.Dir(), backupDirName))
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %v", backups)
	}

	content, err := os.ReadFile(filepath.Join(store.Dir(), backupDirName, backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != v1 {
		t.Fatalf("backup should hold previous content, got %q", content)
	}
}

// 备份数量应被限制在上限内。
func TestBackupRetention(t *testing.T) {
	store := newStore(t)
	for i := 0; i < maxBackups+5; i++ {
		if _, err := store.Save("app.yaml", v1+strings.Repeat("#\n", i)); err != nil {
			t.Fatalf("save %d failed: %v", i, err)
		}
	}
	backups := listDir(t, filepath.Join(store.Dir(), backupDirName))
	if len(backups) > maxBackups {
		t.Fatalf("expected at most %d backups, got %d", maxBackups, len(backups))
	}
}

// 历史记录应包含每次保存。
func TestVersionsRecorded(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("app.yaml", v2); err != nil {
		t.Fatal(err)
	}

	versions, err := store.Versions("app.yaml")
	if err != nil {
		t.Fatalf("Versions failed: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	if versions[0].Action != "save" || versions[1].Action != "create" {
		t.Fatalf("unexpected actions: %+v", versions)
	}
	// 最新在前
	if versions[0].Size != int64(len(v2)) {
		t.Fatalf("newest version should be v2, got size %d", versions[0].Size)
	}
}

// 回滚应恢复旧内容，并把回滚本身记入历史。
func TestRollback(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("app.yaml", v2); err != nil {
		t.Fatal(err)
	}

	versions, err := store.Versions("app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var backupName string
	for _, version := range versions {
		if version.Action == "save" && version.BackupName != "" {
			backupName = version.BackupName
			break
		}
	}
	if backupName == "" {
		t.Fatalf("no backup recorded in history: %+v", versions)
	}

	if err := store.Rollback("app.yaml", backupName); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	content, err := store.Read("app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if content != v1 {
		t.Fatalf("rollback should restore v1, got %q", content)
	}

	versions, _ = store.Versions("app.yaml")
	if versions[0].Action != "rollback" {
		t.Fatalf("expected rollback recorded, got %+v", versions[0])
	}
}

// 回滚前应备份当前内容，避免回滚造成新的丢失。
func TestRollbackBacksUpCurrent(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("app.yaml", v2); err != nil {
		t.Fatal(err)
	}

	versions, _ := store.Versions("app.yaml")
	var backupName string
	for _, version := range versions {
		if version.BackupName != "" {
			backupName = version.BackupName
			break
		}
	}
	before := len(listDir(t, filepath.Join(store.Dir(), backupDirName)))
	if err := store.Rollback("app.yaml", backupName); err != nil {
		t.Fatal(err)
	}
	after := len(listDir(t, filepath.Join(store.Dir(), backupDirName)))
	if after <= before {
		t.Fatalf("rollback should create a backup of current content: %d -> %d", before, after)
	}
}

// 删除是软删除，可在回收站恢复。
func TestSoftDeleteAndRestore(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("app.yaml"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Read("app.yaml"); err == nil {
		t.Fatal("file should be gone after delete")
	}

	items, err := store.ListTrash()
	if err != nil {
		t.Fatalf("ListTrash failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 trash item, got %+v", items)
	}
	if items[0].Original != "app.yaml" {
		t.Fatalf("unexpected original name: %q", items[0].Original)
	}

	if err := store.Restore(items[0].Name); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	content, err := store.Read("app.yaml")
	if err != nil {
		t.Fatalf("file should be restored: %v", err)
	}
	if content != v1 {
		t.Fatalf("restored content mismatch: %q", content)
	}

	items, _ = store.ListTrash()
	if len(items) != 0 {
		t.Fatalf("trash should be empty after restore, got %+v", items)
	}
}

func TestRestoreRejectsTraversal(t *testing.T) {
	store := newStore(t)
	for _, name := range []string{"../evil", "a/b", ".."} {
		if err := store.Restore(name); err == nil {
			t.Errorf("expected Restore(%q) to fail", name)
		}
	}
}

func TestRollbackRejectsInvalidBackupName(t *testing.T) {
	store := newStore(t)
	if _, err := store.Save("app.yaml", v1); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../evil.bak", "sub/x.bak", ".."} {
		if err := store.Rollback("app.yaml", name); err == nil {
			t.Errorf("expected Rollback(%q) to fail", name)
		}
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && !strings.HasSuffix(entry.Name(), ".tmp") {
			names = append(names, entry.Name())
		}
	}
	return names
}
