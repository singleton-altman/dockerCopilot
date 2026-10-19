package composefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/loader"
	composeType "github.com/compose-spec/compose-go/types"
)

var (
	// 仅允许字母、数字、下划线、短横线和点；不允许路径分隔符。
	fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	allowedExts     = map[string]bool{".yml": true, ".yaml": true}
)

const (
	// backupDirName 保存前自动备份的目录
	backupDirName = ".backups"
	// trashDirName 删除时的软删除目录
	trashDirName = ".trash"
	// historyFileName 版本历史记录
	historyFileName = ".history.jsonl"
	// maxBackups 单个文件最多保留的备份份数
	maxBackups = 20
	// maxHistory 历史记录最多保留条数
	maxHistory = 200
	// historyTimeLayout 带纳秒的时间格式，保证同一秒内的顺序稳定
	historyTimeLayout = "2006-01-02 15:04:05.000000000"
)

// FileInfo 列表项
type FileInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updatedAt"`
}

// Version 一次保存的版本快照
type Version struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
	// Action 触发动作：save / create / rollback
	Action string `json:"action"`
	// BackupName 对应的备份文件名，可用于回滚
	BackupName string `json:"backupName,omitempty"`
}

// TrashItem 软删除的项目
type TrashItem struct {
	Name      string `json:"name"`
	DeletedAt string `json:"deletedAt"`
	Original  string `json:"original"`
}

// Store 管理 stacks 目录下的 compose 文件
type Store struct {
	mu  sync.Mutex
	dir string
}

func NewStore(dir string) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = "/data/stacks"
	}
	return &Store{dir: dir}
}

// Dir 返回当前工作目录
func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, backupDirName), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.dir, trashDirName), 0o755)
}

// resolve 校验文件名并返回绝对路径。
// 只允许单层、带 .yml/.yaml 扩展名的安全文件名，防止目录穿越。
func (s *Store) resolve(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("文件名不能为空")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("非法文件名")
	}
	if strings.HasPrefix(name, ".") {
		return "", errors.New("非法文件名")
	}
	if !fileNamePattern.MatchString(name) {
		return "", errors.New("文件名只能包含字母、数字、点、下划线和短横线")
	}
	if !allowedExts[strings.ToLower(filepath.Ext(name))] {
		return "", errors.New("仅支持 .yml 或 .yaml 文件")
	}

	base, err := filepath.Abs(s.dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	// 双保险：解析后的路径必须仍在工作目录内
	if filepath.Dir(full) != base {
		return "", errors.New("非法文件名")
	}
	return full, nil
}

// List 列出 compose 文件
func (s *Store) List() ([]FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileInfo{}, nil
		}
		return nil, err
	}

	items := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !allowedExts[strings.ToLower(filepath.Ext(entry.Name()))] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, FileInfo{
			Name:      entry.Name(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// Read 读取文件内容
func (s *Store) Read(name string) (string, error) {
	path, err := s.resolve(name)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("文件不存在: %s", name)
		}
		return "", err
	}
	return string(content), nil
}

// Save 写入文件。写入前会做 YAML 结构校验，并把旧内容备份一份。
// 返回是否为新建文件。
func (s *Store) Save(name, content string) (bool, error) {
	path, err := s.resolve(name)
	if err != nil {
		return false, err
	}
	if err := Validate(content); err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureDir(); err != nil {
		return false, err
	}

	// 已存在则先备份旧内容，保证任何覆盖都可回溯
	created := true
	backupName := ""
	if previous, readErr := os.ReadFile(path); readErr == nil {
		created = false
		backupName, err = s.backupLocked(name, previous)
		if err != nil {
			// 备份失败时不继续覆盖，避免无保护的破坏性写入
			return false, fmt.Errorf("备份旧版本失败，已取消保存: %w", err)
		}
	}

	// 先写临时文件再 rename，避免中断留下半截配置
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}

	action := "save"
	if created {
		action = "create"
	}
	_ = s.appendHistoryLocked(Version{
		Filename:   name,
		Size:       int64(len(content)),
		CreatedAt:  time.Now().Format(historyTimeLayout),
		Action:     action,
		BackupName: backupName,
	})
	return created, nil
}

// Delete 将文件移入回收站，而不是直接删除。
func (s *Store) Delete(name string) error {
	path, err := s.resolve(name)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("文件不存在: %s", name)
		}
		return err
	}
	if info.IsDir() {
		return errors.New("不能删除目录")
	}

	if err := s.ensureDir(); err != nil {
		return err
	}

	deletedAt := time.Now()
	trashName := fmt.Sprintf("%s.%s", name, deletedAt.Format("20060102-150405"))
	trashPath := filepath.Join(s.dir, trashDirName, trashName)
	if err := os.Rename(path, trashPath); err != nil {
		return err
	}

	_ = s.appendHistoryLocked(Version{
		Filename:  name,
		Size:      info.Size(),
		CreatedAt: deletedAt.Format(historyTimeLayout),
		Action:    "delete",
	})
	return nil
}

// ListTrash 返回回收站内容
func (s *Store) ListTrash() ([]TrashItem, error) {
	dir := filepath.Join(s.dir, trashDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TrashItem{}, nil
		}
		return nil, err
	}

	items := make([]TrashItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// 文件名格式：{name}.{yyyyMMdd-HHmmss}
		original := entry.Name()
		parts := strings.Split(entry.Name(), ".")
		if len(parts) >= 3 {
			original = strings.Join(parts[:len(parts)-1], ".")
		} else if len(parts) >= 2 {
			original = parts[0] + "." + parts[1]
		}
		items = append(items, TrashItem{
			Name:      entry.Name(),
			DeletedAt: info.ModTime().Format("2006-01-02 15:04:05"),
			Original:  original,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeletedAt > items[j].DeletedAt })
	return items, nil
}

// Restore 从回收站恢复
func (s *Store) Restore(trashName string) error {
	if trashName != filepath.Base(trashName) || strings.ContainsAny(trashName, `/\`) || strings.Contains(trashName, "..") {
		return errors.New("非法文件名")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	trashPath := filepath.Join(s.dir, trashDirName, trashName)
	content, err := os.ReadFile(trashPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("回收站中不存在该文件")
		}
		return err
	}

	// 还原目标名：去掉尾部时间戳
	target := trashName
	if idx := strings.LastIndex(target, "."); idx > 0 {
		target = target[:idx]
	}
	if _, err := s.resolve(target); err != nil {
		return fmt.Errorf("还原目标名无效: %w", err)
	}

	// 目标已存在时先备份
	targetPath := filepath.Join(s.dir, target)
	if previous, readErr := os.ReadFile(targetPath); readErr == nil {
		if _, err := s.backupLocked(target, previous); err != nil {
			return fmt.Errorf("备份现有文件失败: %w", err)
		}
	}

	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return err
	}
	_ = os.Remove(trashPath)
	_ = s.appendHistoryLocked(Version{
		Filename:  target,
		Size:      int64(len(content)),
		CreatedAt: time.Now().Format(historyTimeLayout),
		Action:    "restore",
	})
	return nil
}

// Versions 返回某个文件的历史版本（最新在前）
func (s *Store) Versions(name string) ([]Version, error) {
	if _, err := s.resolve(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readHistoryLocked(name)
}

// Rollback 回滚到指定备份
func (s *Store) Rollback(name, backupName string) error {
	if _, err := s.resolve(name); err != nil {
		return err
	}
	if backupName != filepath.Base(backupName) || strings.Contains(backupName, "..") {
		return errors.New("非法备份文件名")
	}
	backupPath := filepath.Join(s.dir, backupDirName, backupName)
	if filepath.Dir(backupPath) != filepath.Join(s.dir, backupDirName) {
		return errors.New("非法备份文件名")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	content, err := os.ReadFile(backupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("备份不存在")
		}
		return err
	}
	if err := Validate(string(content)); err != nil {
		return fmt.Errorf("备份内容已失效: %w", err)
	}

	// 回滚前把当前内容也备份，避免回滚本身造成丢失
	targetPath := filepath.Join(s.dir, name)
	if previous, readErr := os.ReadFile(targetPath); readErr == nil {
		if _, err := s.backupLocked(name, previous); err != nil {
			return fmt.Errorf("备份当前版本失败: %w", err)
		}
	}

	if err := os.WriteFile(targetPath, content, 0o644); err != nil {
		return err
	}
	_ = s.appendHistoryLocked(Version{
		Filename:   name,
		Size:       int64(len(content)),
		CreatedAt:  time.Now().Format(historyTimeLayout),
		Action:     "rollback",
		BackupName: backupName,
	})
	return nil
}

// backupLocked 写入一份带时间戳的备份，并清理超出上限的旧备份。
// 返回备份文件名。调用方必须已持有 s.mu。
func (s *Store) backupLocked(name string, content []byte) (string, error) {
	dir := filepath.Join(s.dir, backupDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405.000000000")
	backupName := fmt.Sprintf("%s.%s.bak", name, stamp)
	// 临时文件 + rename，避免备份本身写坏
	tmp := filepath.Join(dir, backupName+".tmp")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dir, backupName)); err != nil {
		return "", err
	}

	// 清理超出上限的旧备份
	entries, err := os.ReadDir(dir)
	if err != nil {
		return backupName, nil
	}
	prefix := name + "."
	var backups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) && strings.HasSuffix(entry.Name(), ".bak") {
			backups = append(backups, entry.Name())
		}
	}
	sort.Strings(backups)
	for len(backups) > maxBackups {
		_ = os.Remove(filepath.Join(dir, backups[0]))
		backups = backups[1:]
	}
	return backupName, nil
}

// appendHistoryLocked 追加一条版本记录，并修剪到上限。
// 调用方必须已持有 s.mu。
func (s *Store) appendHistoryLocked(version Version) error {
	path := filepath.Join(s.dir, historyFileName)

	// 读取既有记录
	var versions []Version
	if content, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var v Version
			if json.Unmarshal([]byte(line), &v) == nil {
				versions = append(versions, v)
			}
		}
	}

	versions = append(versions, version)
	if len(versions) > maxHistory {
		versions = versions[len(versions)-maxHistory:]
	}

	var builder strings.Builder
	for _, v := range versions {
		line, err := json.Marshal(v)
		if err != nil {
			continue
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(builder.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readHistoryLocked 读取并按时间倒序返回。
func (s *Store) readHistoryLocked(name string) ([]Version, error) {
	path := filepath.Join(s.dir, historyFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Version{}, nil
		}
		return nil, err
	}

	versions := make([]Version, 0)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v Version
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if v.Filename == name {
			versions = append(versions, v)
		}
	}
	// 最新在前
	sort.Slice(versions, func(i, j int) bool { return versions[i].CreatedAt > versions[j].CreatedAt })
	return versions, nil
}

// Validate 校验 compose YAML 结构。
// 返回的错误只描述结构问题，不回显文件内容，避免把敏感配置写进日志/响应。
func Validate(content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("内容不能为空")
	}
	if len(content) > 1<<20 {
		return errors.New("文件过大（上限 1MB）")
	}

	config, err := loader.ParseYAML([]byte(content))
	if err != nil {
		return fmt.Errorf("YAML 解析失败: %s", sanitizeErr(err))
	}

	services, ok := config["services"]
	if !ok {
		return errors.New("缺少 services 字段")
	}
	serviceMap, ok := services.(map[string]interface{})
	if !ok || len(serviceMap) == 0 {
		return errors.New("services 至少要包含一个服务")
	}

	// 用 compose-go 做一次完整加载，捕获类型错误。
	// 项目名只用于解析，不参与校验结果，这里给一个固定占位值。
	if _, err := loader.Load(composeType.ConfigDetails{
		ConfigFiles: []composeType.ConfigFile{{Content: []byte(content)}},
	}, func(options *loader.Options) {
		options.SkipValidation = false
		options.SetProjectName("compose-validation", false)
	}); err != nil {
		return fmt.Errorf("compose 结构校验失败: %s", sanitizeErr(err))
	}
	return nil
}

// sanitizeErr 截断并去掉换行，避免响应里出现大段内容。
func sanitizeErr(err error) string {
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}
