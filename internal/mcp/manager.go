package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// RuntimeConfig MCP 运行时配置：开关与访问密钥。
// 热更新（atomic 指针替换），Handler 每次请求读取最新值，无需重启。
type RuntimeConfig struct {
	Enabled   bool   `json:"enabled"`
	AccessKey string `json:"accessKey"`
}

// Manager 管理 MCP 运行时配置：内存原子态 + /data 持久化。
type Manager struct {
	config atomic.Pointer[RuntimeConfig]
	path   string
}

// NewManager 创建配置管理器。
//   - path: 持久化文件路径（如 /data/config/mcp-config.json）
//   - envEnabled / envAccessKey: 环境变量初始值（文件存在时文件优先）
//
// 加载顺序：文件 > 环境变量 > 默认关闭。加载失败时回退默认值（关闭），
// 不阻塞主服务启动；持久化失败只记录，下次保存重试。
func NewManager(path, envEnabled, envAccessKey string) *Manager {
	m := &Manager{path: path}
	cfg := m.load()
	if cfg == nil {
		cfg = &RuntimeConfig{
			Enabled:   envEnabled == "true",
			AccessKey: strings.TrimSpace(envAccessKey),
		}
		if cfg.AccessKey == "" {
			cfg.Enabled = false
		}
	}
	m.config.Store(cfg)
	return m
}

// Current 返回当前配置快照。
func (m *Manager) Current() RuntimeConfig {
	cfg := m.config.Load()
	if cfg == nil {
		return RuntimeConfig{}
	}
	return *cfg
}

// Update 校验并热更新配置，成功后持久化。
// 不传密钥（空串）时保留旧密钥，方便开关切换与再次开启无需重填。
func (m *Manager) Update(enabled bool, accessKey string) error {
	key := strings.TrimSpace(accessKey)
	if key == "" {
		key = m.Current().AccessKey
	}
	if enabled && len(key) < 8 {
		return errors.New("访问密钥至少 8 位")
	}
	cfg := &RuntimeConfig{Enabled: enabled, AccessKey: key}
	m.config.Store(cfg)
	return m.persist(cfg)
}

// Enabled 返回当前是否启用（Handler 每次请求读取）。
func (m *Manager) Enabled() bool { return m.Current().Enabled }

// AccessKey 返回当前访问密钥。
func (m *Manager) AccessKey() string { return m.Current().AccessKey }

// load 从磁盘读配置；文件不存在或损坏返回 nil。
func (m *Manager) load() *RuntimeConfig {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return nil
	}
	var cfg RuntimeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func (m *Manager) persist(cfg *RuntimeConfig) error {
	// path 为空表示纯内存模式（测试用），只改内存态
	if m.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
