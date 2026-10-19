package composefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// QueueDirName 队列目录名，位于 compose 工作目录下。
	// watcher 脚本会监视该目录并执行请求。
	QueueDirName = ".queue"
	// ResultDirName 执行结果目录名。
	ResultDirName = ".results"
)

// Action 允许的编排动作。
// 服务端不会自己执行，只负责投递请求。
type Action string

const (
	ActionUp      Action = "up"      // docker compose up -d
	ActionDown    Action = "down"    // docker compose down
	ActionRestart Action = "restart" // docker compose restart
	ActionPull    Action = "pull"    // docker compose pull
)

var validActions = map[Action]bool{
	ActionUp:      true,
	ActionDown:    true,
	ActionRestart: true,
	ActionPull:    true,
}

// Job 投递给宿主机 watcher 的执行请求
type Job struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Action    Action    `json:"action"`
	CreatedAt time.Time `json:"createdAt"`
}

// JobResult watcher 回写的结果
type JobResult struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	Action     Action    `json:"action"`
	Success    bool      `json:"success"`
	Output     string    `json:"output"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Executor 负责投递请求与读取结果。
// 真正的 docker compose 命令由宿主机侧的 watcher 执行。
type Executor struct {
	mu         sync.Mutex
	workDir    string
	queueDir   string
	resultDir  string
	maxPending int
	// maxOutput 限制回写结果的输出长度，避免异常输出撑爆响应
	maxOutput int
}

func NewExecutor(workDir string) *Executor {
	return &Executor{
		workDir:    workDir,
		queueDir:   filepath.Join(workDir, QueueDirName),
		resultDir:  filepath.Join(workDir, ResultDirName),
		maxPending: 16,
		maxOutput:  8 * 1024,
	}
}

func (e *Executor) ensureDirs() error {
	if err := os.MkdirAll(e.queueDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(e.resultDir, 0o755)
}

// Submit 投递一个执行请求，返回请求 ID。
func (e *Executor) Submit(filename string, action Action) (string, error) {
	if !validActions[action] {
		return "", fmt.Errorf("不支持的执行动作: %s", action)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.ensureDirs(); err != nil {
		return "", err
	}

	// 避免请求堆积：队列里过期的、watcher 未运行的请求不应无限增长
	pending, err := e.pendingCountLocked()
	if err != nil {
		return "", err
	}
	if pending >= e.maxPending {
		return "", errors.New("待执行请求过多，请等待宿主机 watcher 处理")
	}

	id := fmt.Sprintf("%d-%s", time.Now().UnixNano(), sanitizeID(filename))
	job := Job{
		ID:        id,
		Filename:  filename,
		Action:    action,
		CreatedAt: time.Now(),
	}
	content, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return "", err
	}

	// 先写临时文件再 rename，watcher 不会读到半截请求
	path := filepath.Join(e.queueDir, id+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return id, nil
}

// Result 读取某个请求的执行结果。尚未完成时 done 为 false。
func (e *Executor) Result(id string) (JobResult, bool, error) {
	if !isSafeID(id) {
		return JobResult{}, false, errors.New("非法请求 ID")
	}
	path := filepath.Join(e.resultDir, id+".json")
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return JobResult{}, false, nil
		}
		return JobResult{}, false, err
	}

	var result JobResult
	if err := json.Unmarshal(content, &result); err != nil {
		return JobResult{}, false, fmt.Errorf("解析执行结果失败: %w", err)
	}
	if len(result.Output) > e.maxOutput {
		result.Output = result.Output[:e.maxOutput] + "\n...(已截断)"
	}
	return result, true, nil
}

// Recent 返回最近的结果，便于展示执行历史。
func (e *Executor) Recent(limit int) ([]JobResult, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	entries, err := os.ReadDir(e.resultDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []JobResult{}, nil
		}
		return nil, err
	}

	results := make([]JobResult, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(e.resultDir, entry.Name()))
		if err != nil {
			continue
		}
		var result JobResult
		if json.Unmarshal(content, &result) != nil {
			continue
		}
		if len(result.Output) > 2000 {
			result.Output = result.Output[:2000] + "..."
		}
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].FinishedAt.After(results[j].FinishedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (e *Executor) pendingCountLocked() (int, error) {
	entries, err := os.ReadDir(e.queueDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

// sanitizeID 只保留安全字符，用于拼接请求 ID。
func sanitizeID(filename string) string {
	var b strings.Builder
	for _, r := range filename {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "job"
	}
	return b.String()
}

// isSafeID 校验请求 ID 只含安全字符，防止路径穿越。
func isSafeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !valid {
			return false
		}
	}
	return !strings.Contains(id, "..")
}
