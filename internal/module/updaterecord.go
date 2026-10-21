package module

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
)

// 更新阶段
const (
	PhasePulling = "pulling"
	PhaseStopped = "stopped"
	PhaseRenamed = "renamed"
	PhaseCreated = "created"
	PhaseStarted = "started"
	PhaseDone    = "done"
	// PhaseCanceled 表示在拉取阶段被中断，容器未被任何改动。
	PhaseCanceled = "canceled"
	PhaseFailed   = "failed"
)

// 更新状态
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// ContainerUpdateRecord 一次容器更新的状态记录，供列表展示与中断判断。
type ContainerUpdateRecord struct {
	ID              string `json:"id"`
	ContainerName   string `json:"containerName"`
	OriginalID      string `json:"originalId"`
	OriginalName    string `json:"originalName"`
	OriginalImage   string `json:"originalImage"`
	RenamedTo       string `json:"renamedTo,omitempty"`
	NewContainerID  string `json:"newContainerId,omitempty"`
	ImageNameAndTag string `json:"imageNameAndTag"`
	// WasRunning 更新前容器是否在运行。
	WasRunning bool      `json:"wasRunning"`
	Phase      string    `json:"phase"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	// CancelRequested 由取消接口置位，更新协程在拉取阶段检查。
	CancelRequested bool `json:"-"`
}

// UpdateRecordStore 更新记录存储。按容器名索引（更新期间容器名不变，ID 会变），
// 同时保留最近的历史记录供列表展示。
type UpdateRecordStore struct {
	mu      sync.RWMutex
	path    string
	byName  map[string]*ContainerUpdateRecord
	history []*ContainerUpdateRecord
	loaded  bool
}

const updateRecordHistoryLimit = 50

func NewUpdateRecordStore(path string) *UpdateRecordStore {
	return &UpdateRecordStore{
		path:   path,
		byName: make(map[string]*ContainerUpdateRecord),
	}
}

type updateRecordFile struct {
	Records []*ContainerUpdateRecord `json:"records"`
}

// Begin 创建一条更新记录并落盘，返回记录副本。
func (s *UpdateRecordStore) Begin(rec *ContainerUpdateRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	now := time.Now()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	s.byName[rec.ContainerName] = rec
	s.appendHistoryLocked(rec)
	s.saveLocked()
}

// Get 按容器名取当前记录。
func (s *UpdateRecordStore) Get(name string) (*ContainerUpdateRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureLoadedLocked()
	rec, ok := s.byName[name]
	return rec, ok
}

// Snapshot 返回记录的副本，避免调用方在锁外读到并发修改的字段。
func (s *UpdateRecordStore) Snapshot(name string) (ContainerUpdateRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureLoadedLocked()
	rec, ok := s.byName[name]
	if !ok {
		return ContainerUpdateRecord{}, false
	}
	return *rec, true
}

// Update 以回调方式修改记录并落盘。
func (s *UpdateRecordStore) Update(name string, fn func(*ContainerUpdateRecord)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	rec, ok := s.byName[name]
	if !ok {
		return false
	}
	fn(rec)
	rec.UpdatedAt = time.Now()
	s.saveLocked()
	return true
}

// RequestCancel 标记取消意图，返回 false 表示记录不存在或已完成。
func (s *UpdateRecordStore) RequestCancel(name string) (ContainerUpdateRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoadedLocked()
	rec, ok := s.byName[name]
	if !ok {
		return ContainerUpdateRecord{}, false
	}
	rec.CancelRequested = true
	rec.UpdatedAt = time.Now()
	s.saveLocked()
	return *rec, true
}

// IsCancelRequested 由更新协程在阶段边界轮询。
func (s *UpdateRecordStore) IsCancelRequested(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureLoadedLocked()
	rec, ok := s.byName[name]
	return ok && rec.CancelRequested
}

// Clear 移除容器名索引（保留历史记录）。
func (s *UpdateRecordStore) Clear(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byName, name)
	s.saveLocked()
}

// List 返回当前活跃记录 + 历史（去重，按创建时间倒序）。
func (s *UpdateRecordStore) List() []ContainerUpdateRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureLoadedLocked()
	seen := make(map[string]bool)
	out := make([]ContainerUpdateRecord, 0, len(s.history))
	for _, rec := range s.history {
		if seen[rec.ID] {
			continue
		}
		seen[rec.ID] = true
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > updateRecordHistoryLimit {
		out = out[:updateRecordHistoryLimit]
	}
	return out
}

func (s *UpdateRecordStore) appendHistoryLocked(rec *ContainerUpdateRecord) {
	s.history = append([]*ContainerUpdateRecord{rec}, s.history...)
	if len(s.history) > updateRecordHistoryLimit {
		s.history = s.history[:updateRecordHistoryLimit]
	}
}

func (s *UpdateRecordStore) ensureLoadedLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			logx.Errorf("读取更新记录失败: %v", err)
		}
		return
	}
	var file updateRecordFile
	if err := json.Unmarshal(data, &file); err != nil {
		logx.Errorf("解析更新记录失败: %v", err)
		return
	}
	s.history = file.Records
	for _, rec := range file.Records {
		// 重启后不再有协程在跑：遗留的 running 记录视为失败，允许回撤。
		if rec.Status == StatusRunning {
			rec.Status = StatusFailed
			rec.Error = "服务重启，更新未完成"
			rec.Phase = PhaseFailed
		}
		// history 按时间倒序存放，同一容器必须保留最新一条；
		// 直接赋值会被后面的旧记录覆盖，导致回撤读到过期状态。
		if rec.ContainerName == "" {
			continue
		}
		if _, exists := s.byName[rec.ContainerName]; !exists {
			s.byName[rec.ContainerName] = rec
		}
	}
}

func (s *UpdateRecordStore) saveLocked() {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		logx.Errorf("创建更新记录目录失败: %v", err)
		return
	}
	data, err := json.MarshalIndent(updateRecordFile{Records: s.history}, "", "  ")
	if err != nil {
		logx.Errorf("序列化更新记录失败: %v", err)
		return
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		logx.Errorf("写入更新记录失败: %v", err)
	}
}
