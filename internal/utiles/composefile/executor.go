package composefile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
)

const (
	// QueueDirName 队列目录名，位于 compose 工作目录下。
	// 内置执行器或宿主机 watcher 会监视该目录并执行请求。
	QueueDirName = ".queue"
	// ResultDirName 执行结果目录名。
	ResultDirName = ".results"
	// claimSuffix 任务被消费者认领后的文件后缀。
	claimSuffix = ".claim"
)

const (
	// jobMaxAge 超过该时限的请求视为过期，不再执行。
	jobMaxAge = 5 * time.Minute
	// composeExecTimeout 单个 compose 命令的执行超时。
	composeExecTimeout = 10 * time.Minute
	// resultKeep 结果目录最多保留的结果条数。
	resultKeep = 50
	// queuePollInterval 内置执行器扫描队列的间隔。
	queuePollInterval = time.Second
)

// Action 允许的编排动作。
// 服务端不会自己执行，只负责投递请求。
type Action string

const (
	ActionUp      Action = "up"      // docker compose up -d
	ActionDown    Action = "down"    // docker compose down
	ActionRestart Action = "restart" // docker compose restart
	ActionPull    Action = "pull"    // docker compose pull
	// ActionDeploy 拉取镜像后启动，一次任务完成「更新并生效」
	ActionDeploy Action = "deploy"
)

var validActions = map[Action]bool{
	ActionUp:      true,
	ActionDown:    true,
	ActionRestart: true,
	ActionPull:    true,
	ActionDeploy:  true,
}

// Job 投递给宿主机 watcher 的执行请求
type Job struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Action    Action    `json:"action"`
	CreatedAt time.Time `json:"createdAt"`
	// ignorePullFailures 仅进程内使用，不参与序列化：
	// deploy 复合动作里的 pull 允许个别镜像失败，避免 build-only 服务拖垮整次部署。
	ignorePullFailures bool
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
// 执行优先由内置执行器完成（容器内 docker CLI 通过挂载的 docker.sock 操作宿主机），
// 宿主机侧的 watcher 若存在亦可消费同一队列，双方靠原子认领互不冲突。
type Executor struct {
	mu         sync.Mutex
	workDir    string
	queueDir   string
	resultDir  string
	maxPending int
	// maxOutput 限制回写结果的输出长度，避免异常输出撑爆响应
	maxOutput int
	// composeCmd 探测到的 compose 调用方式，如 ["docker","compose"] 或 ["/app/bin/docker-compose"]
	composeCmd []string
	// runner 执行单个任务，测试时可替换。
	runner func(ctx context.Context, job Job) JobResult
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

// Start 启动内置执行器，在进程生命周期内轮询队列并执行任务。
// 容器镜像自带 docker CLI 与 compose 插件，通过挂载的 docker.sock 操作宿主机 Docker，
// 因此普通安装不再需要宿主机侧的 watcher。CLI 不可用（如本地开发环境）时只记录日志并返回，
// 不消费队列，任务留给外部 watcher 处理。
func (e *Executor) Start(ctx context.Context) {
	e.composeCmd = detectCompose()
	if e.composeCmd == nil {
		logx.Info("容器内 docker compose 不可用，执行请求将等待宿主机 watcher 处理")
		return
	}
	go func() {
		ticker := time.NewTicker(queuePollInterval)
		defer ticker.Stop()
		for {
			e.drainOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	logx.Infof("内置 compose 执行器已启动: %s", strings.Join(e.composeCmd, " "))
}

// composeCandidates 依次探测的 compose 调用方式。
// 除镜像内安装的 docker CLI 外，也支持 /app/bin 下的静态二进制，
// 便于热更新场景（旧镜像没有 apk 安装的 CLI，只有随更新包放入的二进制）。
var composeCandidates = [][]string{
	{"docker", "compose"},
	{"/app/bin/docker", "compose"},
	{"docker-compose"},
	{"/app/bin/docker-compose"},
}

// detectCompose 返回第一个可用的 compose 调用方式，均不可用时返回 nil。
func detectCompose() []string {
	for _, candidate := range composeCandidates {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		args := append(append([]string{}, candidate[1:]...), "version")
		err := exec.CommandContext(ctx, candidate[0], args...).Run()
		cancel()
		if err == nil {
			return candidate
		}
	}
	return nil
}

// drainOnce 认领并处理当前队列里的所有任务。
func (e *Executor) drainOnce(ctx context.Context) {
	// 内置执行器不可用且未注入 runner（测试）时不认领，任务留给外部 watcher
	if e.composeCmd == nil && e.runner == nil {
		return
	}
	entries, err := os.ReadDir(e.queueDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !isSafeID(id) {
			continue
		}
		e.claimAndRun(ctx, id)
	}
}

// claimAndRun 原子认领一个任务并执行。
// rename 是原子的：内置执行器与外部 watcher 谁先改名为 .claim 谁执行，不会重复执行。
func (e *Executor) claimAndRun(ctx context.Context, id string) {
	queuePath := filepath.Join(e.queueDir, id+".json")
	claimPath := queuePath + claimSuffix
	if err := os.Rename(queuePath, claimPath); err != nil {
		// 已被其他消费者取走
		return
	}
	content, err := os.ReadFile(claimPath)
	if err != nil {
		return
	}
	defer os.Remove(claimPath)

	var job Job
	if err := json.Unmarshal(content, &job); err != nil {
		e.writeResult(JobResult{ID: id, Success: false,
			Output: "任务文件损坏: " + err.Error(), FinishedAt: time.Now()})
		return
	}
	// 消费侧二次校验，不信任投递阶段写入的任何内容
	if !validActions[job.Action] || !isExecutableFilename(job.Filename) {
		e.writeResult(JobResult{ID: id, Filename: job.Filename, Action: job.Action, Success: false,
			Output:     fmt.Sprintf("非法执行请求: action=%s filename=%s", job.Action, job.Filename),
			FinishedAt: time.Now()})
		return
	}
	if time.Since(job.CreatedAt) > jobMaxAge {
		e.writeResult(JobResult{ID: id, Filename: job.Filename, Action: job.Action, Success: false,
			Output: "任务已过期，未执行", FinishedAt: time.Now()})
		return
	}

	runner := e.runner
	if runner == nil {
		runner = e.runJob
	}
	logx.Infof("开始执行 compose 任务: %s %s (job %s)", job.Action, job.Filename, job.ID)
	result := runner(ctx, job)
	result.ID = id
	result.Filename = job.Filename
	result.Action = job.Action
	if result.FinishedAt.IsZero() {
		result.FinishedAt = time.Now()
	}
	if len(result.Output) > e.maxOutput {
		result.Output = result.Output[:e.maxOutput] + "\n...(已截断)"
	}
	e.writeResult(result)
}

// runCompose 通过容器内的 docker CLI 执行 compose 命令，
// daemon 是挂载进容器的宿主机 docker.sock，效果等同于在宿主机上执行。
func (e *Executor) runCompose(ctx context.Context, job Job) JobResult {
	execCtx, cancel := context.WithTimeout(ctx, composeExecTimeout)
	defer cancel()

	// composeCmd[0] 是二进制本体，由 exec.Command 的 path 参数承担，args 从子命令开始
	args := append([]string{}, e.composeCmd[1:]...)
	args = append(args, "-f", filepath.Join(e.workDir, job.Filename),
		"--project-directory", e.workDir)
	switch job.Action {
	case ActionUp:
		args = append(args, "up", "-d", "--remove-orphans")
	case ActionDown:
		args = append(args, "down")
	case ActionRestart:
		args = append(args, "restart")
	case ActionPull:
		args = append(args, "pull")
		if job.ignorePullFailures {
			args = append(args, "--ignore-pull-failures")
		}
	}

	cmd := exec.CommandContext(execCtx, e.composeCmd[0], args...)
	output, err := cmd.CombinedOutput()
	result := JobResult{Success: err == nil, Output: string(output), FinishedAt: time.Now()}
	if err != nil {
		result.Output = fmt.Sprintf("%s\n执行失败: %v", result.Output, err)
	}
	if execCtx.Err() == context.DeadlineExceeded {
		result.Output += "\n执行超时，已终止"
	}
	if result.Success {
		logx.Infof("compose 任务完成: %s %s (job %s)", job.Action, job.Filename, job.ID)
	} else {
		logx.Errorf("compose 任务失败: %s %s (job %s)", job.Action, job.Filename, job.ID)
	}
	return result
}

// runJob 执行一个任务。deploy 是复合动作：先拉取镜像再启动，
// 让「更新镜像并生效」在界面上是一次操作，而不是先点拉取再点启动。
func (e *Executor) runJob(ctx context.Context, job Job) JobResult {
	if job.Action != ActionDeploy {
		return e.runCompose(ctx, job)
	}

	pullJob := job
	pullJob.Action = ActionPull
	pullJob.ignorePullFailures = true
	pullResult := e.runCompose(ctx, pullJob)

	upJob := job
	upJob.Action = ActionUp
	upResult := e.runCompose(ctx, upJob)

	result := JobResult{
		Output:     "== pull ==\n" + pullResult.Output + "\n\n== up ==\n" + upResult.Output,
		Success:    upResult.Success,
		FinishedAt: time.Now(),
	}
	if !pullResult.Success {
		// 拉取失败不阻断启动：只有 build 没有 image 的服务 pull 必然报错，
		// 此时用本地已有镜像启动仍然是有意义的，最终成败以启动为准。
		result.Output += "\n提示: 拉取阶段未完全成功，已使用本地已有镜像启动"
	}
	return result
}

// writeResult 落盘结果并清理过期记录。
func (e *Executor) writeResult(result JobResult) {
	if !isSafeID(result.ID) {
		return
	}
	if err := os.MkdirAll(e.resultDir, 0o755); err != nil {
		logx.Errorf("创建结果目录失败: %v", err)
		return
	}
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(e.resultDir, result.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		logx.Errorf("写入执行结果失败: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logx.Errorf("写入执行结果失败: %v", err)
		return
	}
	e.pruneResults()
}

// pruneResults 只保留最近 resultKeep 条结果。
func (e *Executor) pruneResults() {
	entries, err := os.ReadDir(e.resultDir)
	if err != nil || len(entries) <= resultKeep {
		return
	}
	type item struct {
		name       string
		finishedAt time.Time
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(e.resultDir, entry.Name()))
		if err != nil {
			continue
		}
		var result JobResult
		if json.Unmarshal(content, &result) != nil || result.FinishedAt.IsZero() {
			continue
		}
		items = append(items, item{entry.Name(), result.FinishedAt})
	}
	if len(items) <= resultKeep {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].finishedAt.After(items[j].finishedAt) })
	for _, old := range items[resultKeep:] {
		_ = os.Remove(filepath.Join(e.resultDir, old.name))
	}
}

// isExecutableFilename 任务只允许执行单层 .yml/.yaml 文件。
func isExecutableFilename(name string) bool {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
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
