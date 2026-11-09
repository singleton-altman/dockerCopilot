package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerEnvDefaults(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "mcp.json"), "true", "env-key-12345")
	if !m.Enabled() {
		t.Fatal("should be enabled from env")
	}
	if m.AccessKey() != "env-key-12345" {
		t.Fatalf("key: %q", m.AccessKey())
	}

	// 密钥为空时强制关闭（安全默认）
	off := NewManager("", "true", "")
	if off.Enabled() {
		t.Fatal("empty key must force disable")
	}

	// 无任何来源时默认关闭
	d := NewManager("", "false", "")
	if d.Enabled() {
		t.Fatal("default must be disabled")
	}
}

func TestManagerFileOverridesEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true,"accessKey":"file-key-67890"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path, "false", "env-key-12345")
	if !m.Enabled() || m.AccessKey() != "file-key-67890" {
		t.Fatalf("file should win: enabled=%v key=%q", m.Enabled(), m.AccessKey())
	}
}

func TestManagerCorruptFileFallsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path, "true", "env-key-12345")
	if !m.Enabled() {
		t.Fatal("corrupt file should fall back to env")
	}
}

func TestUpdatePersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	m := NewManager(path, "false", "")
	if err := m.Update(true, "persist-key-123"); err != nil {
		t.Fatal(err)
	}

	// 新 Manager 从同一文件读出刚保存的配置
	m2 := NewManager(path, "false", "")
	if !m2.Enabled() || m2.AccessKey() != "persist-key-123" {
		t.Fatalf("persist failed: enabled=%v key=%q", m2.Enabled(), m2.AccessKey())
	}

	// 文件权限收紧到 600（含密钥）
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm: %v", perm)
	}
}

func TestUpdateValidation(t *testing.T) {
	m := NewManager("", "false", "")
	if err := m.Update(true, "short"); err == nil {
		t.Fatal("enabling with short key must fail")
	}
	if m.Enabled() {
		t.Fatal("failed update must not change state")
	}
	if err := m.Update(true, ""); err == nil {
		t.Fatal("enabling with empty key must fail")
	}
}

func TestDisableKeepsKeyForReenable(t *testing.T) {
	m := NewManager("", "true", "keep-key-12345")
	if err := m.Update(false, ""); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() {
		t.Fatal("must be disabled")
	}
	if m.AccessKey() != "keep-key-12345" {
		t.Fatalf("key should be kept: %q", m.AccessKey())
	}
	// 再次开启无需重填密钥
	if err := m.Update(true, ""); err != nil {
		t.Fatal(err)
	}
	if !m.Enabled() || m.AccessKey() != "keep-key-12345" {
		t.Fatalf("reenable: enabled=%v key=%q", m.Enabled(), m.AccessKey())
	}
}

func TestDisableWithNewKeyUpdatesKey(t *testing.T) {
	m := NewManager("", "true", "old-key-12345")
	if err := m.Update(false, "new-key-67890"); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() {
		t.Fatal("must be disabled")
	}
	if m.AccessKey() != "new-key-67890" {
		t.Fatalf("key: %q", m.AccessKey())
	}
}

func TestPersistedJSONShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	m := NewManager(path, "false", "")
	if err := m.Update(true, "shape-key-123"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg RuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || !strings.Contains(string(data), "accessKey") {
		t.Fatalf("json shape: %s", data)
	}
}
