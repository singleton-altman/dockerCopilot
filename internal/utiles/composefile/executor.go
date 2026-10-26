package composefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/docker/docker/client"
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

// Action 允许的编排动作，由内置执行器或外部 watcher 消费。
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
	Owner     *jobOwner `json:"owner,omitempty"`
	// ConfigSha256 投递时文件内容的 SHA-256（hex 小写），旧 watcher/旧队列文件可缺省。
	// 用于在任务详情里核对「部署的是哪一份内容」：部署后再改动文件即可被发现。
	ConfigSha256 string `json:"configSha256,omitempty"`
	// ignorePullFailures 仅进程内使用，不参与序列化：
	// deploy 复合动作里的 pull 允许个别镜像失败，避免 build-only 服务拖垮整次部署。
	ignorePullFailures bool
}

// JobStep 执行过程中的一个阶段，用于向用户说明「实际发生了什么」。
// Status 取值：success / warning / failed / skipped。
type JobStep struct {
	Stage   string `json:"stage"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// JobResult watcher 回写的结果
type JobResult struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Action   Action `json:"action"`
	Success  bool   `json:"success"`
	Output   string `json:"output"`
	// Steps 结构化的过程记录，便于界面按阶段展示；为空时客户端回退到 Output
	Steps      []JobStep `json:"steps,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
	// Status is optional for legacy watchers; interrupted means outcome is unknown.
	Status JobStatus `json:"status,omitempty"`
	// ConfigSha256 与投递请求（Job）里同名字段对应，便于核对部署时用的配置内容。
	ConfigSha256 string `json:"configSha256,omitempty"`
	// Services up/deploy/restart 成功后按 compose 文件采集到的服务运行状态；
	// down/pull 及采集失败时缺省，旧 watcher 不会产出。
	CreatedAt *time.Time     `json:"createdAt,omitempty"`
	StartedAt *time.Time     `json:"startedAt,omitempty"`
	Services  []ServiceState `json:"services,omitempty"`
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
	// docker 用于精确识别容器归属；为 nil 时跳过孤儿清理（宁可留孤儿也不误删）。
	docker *client.Client
	// dockerAPI 服务状态采集用的只读视图；仅测试注入假实现用，为 nil 时回落到 docker。
	dockerAPI   containerSource
	startupOnce sync.Once
	startOnce   sync.Once
	runMu       sync.Mutex
	startupErr  error
	owner       jobOwner
	ownerLock   *os.File
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

// WithDockerClient 注入 docker client，用于精确识别容器归属。
// 不注入时跳过孤儿清理，宁可留下孤儿容器也不误删。
func (e *Executor) WithDockerClient(cli *client.Client) *Executor {
	e.docker = cli
	return e
}

func (e *Executor) ensureDirs() error {
	if err := os.MkdirAll(e.queueDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(e.resultDir, 0o755)
}

// Submit 投递一个执行请求，返回请求 ID。
func (e *Executor) Submit(filename string, action Action) (string, error) {
	return e.SubmitWithConfig(filename, action, "")
}

// SubmitWithConfig 投递执行请求并记录配置指纹。
// configSha256 是投递时 compose 文件内容的 SHA-256（hex 小写），随队列 JSON 持久化，
// 供任务详情核对「实际部署的是哪一份内容」。
func (e *Executor) SubmitWithConfig(filename string, action Action, configSha256 string) (string, error) {
	if !isExecutableFilename(filename) {
		return "", errors.New("仅支持执行单层 .yml 或 .yaml 文件")
	}
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
		ID:           id,
		Filename:     filename,
		Action:       action,
		CreatedAt:    time.Now(),
		ConfigSha256: configSha256,
	}
	// Publish the request only after its complete JSON has been synced.
	path := filepath.Join(e.queueDir, id+".json")
	if err := atomicJSONMode(path, job, 0o644); err != nil {
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
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".json"+claimSuffix)) {
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
	e.startOnce.Do(func() {
		if err := e.startup(); err != nil {
			logx.Errorf("内置 compose 执行器恢复失败，未启动: %v", err)
			return
		}
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
	})
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
		if ctx.Err() != nil {
			return
		}
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
	e.runMu.Lock()
	defer e.runMu.Unlock()
	if ctx.Err() != nil || !isSafeID(id) {
		return
	}
	if err := e.startup(); err != nil {
		logx.Errorf("内置执行器恢复失败: %v", err)
		return
	}
	queuePath := filepath.Join(e.queueDir, id+".json")
	claimPath := queuePath + claimSuffix
	// Never overwrite an existing claim (including an external watcher's).
	if _, err := os.Lstat(claimPath); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.Rename(queuePath, claimPath); err != nil {
		return
	}
	var job Job
	if err := readJSON(claimPath, &job); err != nil {
		e.finishClaim(claimPath, JobResult{ID: id, Success: false, Status: StatusFailed,
			Output: "任务文件损坏，未执行", FinishedAt: time.Now()})
		return
	}
	job.ID = id
	job.Owner = &e.owner
	// Mark ownership before any Docker invocation. If this fails, retain claim
	// without executing; an unmarked claim is never guessed to be ours.
	if err := atomicJSON(claimPath, job); err != nil {
		logx.Errorf("持久化任务归属失败，未执行: %v", err)
		return
	}
	started := time.Now()
	record := lifecycleRecord{JobView: viewFromJob(job, StatusRunning), Owner: e.owner}
	record.StartedAt, record.UpdatedAt = &started, &started
	if err := atomicJSON(e.lifecyclePath(id), record); err != nil {
		logx.Errorf("持久化任务状态失败，未执行: %v", err)
		return
	}
	finish := func(result JobResult) {
		result.ID, result.Filename, result.Action = id, job.Filename, job.Action
		result.CreatedAt, result.StartedAt = &job.CreatedAt, &started
		result.ConfigSha256 = job.ConfigSha256
		if result.FinishedAt.IsZero() {
			result.FinishedAt = time.Now()
		}
		result.Status = resultStatus(result)
		e.finishClaim(claimPath, result)
	}
	if !validActions[job.Action] || !isExecutableFilename(job.Filename) {
		finish(JobResult{Output: "非法执行请求，未执行"})
		return
	}
	if time.Since(job.CreatedAt) > jobMaxAge {
		finish(JobResult{Output: "任务已过期，未执行"})
		return
	}
	if ctx.Err() != nil {
		finish(interruptedResult("任务被中断；结果不确定，请核验实际状态，勿自动重试"))
		return
	}
	// 投递与执行之间文件可能再次被修改（队列最长可等 jobMaxAge）。
	// 指纹对应投递时内容，执行用的是当前文件；发现漂移在结果里明示，避免核对指纹时误导。
	driftDetected := false
	if job.ConfigSha256 != "" {
		if content, err := os.ReadFile(filepath.Join(e.workDir, job.Filename)); err == nil {
			sum := sha256.Sum256(content)
			driftDetected = hex.EncodeToString(sum[:]) != job.ConfigSha256
		}
	}
	runner := e.runner
	if runner == nil {
		runner = e.runJob
	}
	logx.Infof("开始执行 compose 任务: %s %s (job %s)", job.Action, job.Filename, job.ID)
	result := runner(ctx, job)
	if ctx.Err() != nil {
		result.Success, result.Status = false, StatusInterrupted
		result.Output += "\n执行被中断，结果不确定；请核验实际容器和镜像状态，勿自动重试"
		// 与 services.go 的口径一致：中断=结果不确定，不保留服务快照与 ps 结论。
		result.Services = nil
		kept := result.Steps[:0]
		for _, step := range result.Steps {
			if step.Stage != "ps" {
				kept = append(kept, step)
			}
		}
		result.Steps = kept
	}
	if len(result.Steps) == 0 && result.Status != StatusInterrupted {
		result.Steps = summarizeSingle(job.Action, result.Output, result.Success,
			buildableImages(e.workDir, job.Filename))
	}
	if driftDetected && result.Status != StatusInterrupted {
		result.Steps = append([]JobStep{{Stage: "fingerprint", Status: "warning",
			Message: "文件在投递后、执行前被修改：任务指纹对应投递时内容，本次实际执行的是当前文件内容"}}, result.Steps...)
	}
	if len(result.Output) > e.maxOutput {
		result.Output = result.Output[:e.maxOutput] + "\n...(已截断)"
	}
	finish(result)
}

func (e *Executor) finishClaim(claimPath string, result JobResult) {
	if err := e.writeResult(result); err != nil {
		logx.Errorf("写入执行结果失败，保留 claim 及生命周期供恢复: %v", err)
		return
	}
	_ = os.Remove(claimPath)
	_ = os.Remove(e.lifecyclePath(result.ID))
}

// runCompose 通过容器内的 docker CLI 执行 compose 命令，
// daemon 是挂载进容器的宿主机 docker.sock，效果等同于在宿主机上执行。
func (e *Executor) runCompose(ctx context.Context, job Job) JobResult {
	if ctx.Err() != nil {
		return interruptedResult("执行已中断，结果不确定；请核验实际状态")
	}
	execCtx, cancel := context.WithTimeout(ctx, composeExecTimeout)
	defer cancel()

	// up 之前先做一次精确的孤儿清理，替代 --remove-orphans
	if job.Action == ActionUp {
		e.cleanOwnOrphans(execCtx, job)
	}

	// composeCmd[0] 是二进制本体，由 exec.Command 的 path 参数承担，args 从子命令开始
	args := append([]string{}, e.composeCmd[1:]...)
	args = append(args, "-f", filepath.Join(e.workDir, job.Filename),
		"--project-directory", e.workDir)
	switch job.Action {
	case ActionUp:
		// 不用 --remove-orphans：同一 COMPOSE_DIR 下的多个 compose 文件
		// 默认共用目录名作为 project，该参数会把其他文件的容器当孤儿删掉。
		// 改为 up 前只清理「本文件曾经定义、现在已移除」的服务容器。
		args = append(args, "up", "-d")
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
	if execCtx.Err() != nil {
		result.Success, result.Status = false, StatusInterrupted
		result.Output += "\n执行中断或超时，结果不确定；请核验实际容器和镜像状态，勿自动重试"
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
// up/deploy/restart 成功后额外采集本文件容器的运行状态（stage=ps 的只读快照），
// 采集只走只读接口且任何失败都降级为 warning，不会推翻已经得出的任务结论。
func (e *Executor) runJob(ctx context.Context, job Job) JobResult {
	if job.Action != ActionDeploy {
		result := e.runCompose(ctx, job)
		if e.serviceSnapshotApplies(job, result) {
			// 步骤口径与 claimAndRun 的兜底归纳保持一致，再在末尾补 ps 结论
			result.Steps = summarizeSingle(job.Action, result.Output, result.Success,
				buildableImages(e.workDir, job.Filename))
			result = e.attachServiceSnapshot(ctx, job, result)
		}
		return result
	}

	pullJob := job
	pullJob.Action = ActionPull
	pullJob.ignorePullFailures = true
	pullResult := e.runCompose(ctx, pullJob)
	// Cancellation/timeout is not a pull failure that permits a mutating up.
	if ctx.Err() != nil || pullResult.Status == StatusInterrupted {
		pullResult.Success, pullResult.Status = false, StatusInterrupted
		pullResult.Steps = []JobStep{{Stage: "pull", Status: "warning", Message: "拉取被中断，结果不确定"},
			{Stage: "up", Status: "skipped", Message: "任务已中断，未开始启动阶段；请核验实际状态"}}
		return pullResult
	}

	upJob := job
	upJob.Action = ActionUp
	upResult := e.runCompose(ctx, upJob)

	result := JobResult{
		Output:     "== pull ==\n" + pullResult.Output + "\n\n== up ==\n" + upResult.Output,
		Success:    upResult.Success,
		Status:     upResult.Status,
		FinishedAt: time.Now(),
	}
	// 用 pull 的输出判断「到底有没有拉到新镜像」，而不是看退出码：
	// --ignore-pull-failures 让 pull 恒返回 0，退出码反映不了真实结果。
	// 声明了 build 的镜像 registry 上本来就没有，拉取失败是预期，不计入警告。
	buildable := buildableImages(e.workDir, job.Filename)
	pullStep, outcomes := summarizePull(pullResult.Output, buildable)
	// 命令本身失败（--ignore-pull-failures 吞不掉的情形）必须标成 failed
	if !pullResult.Success {
		pullStep.Status = "failed"
	}
	upStep := summarizeUp(upResult.Output)
	if !upResult.Success {
		upStep.Status = "failed"
	}
	result.Steps = []JobStep{pullStep, upStep}

	if pullStep.Status == "warning" || pullStep.Status == "failed" {
		// deploy 里 pull 失败不阻断启动，这里补上后续行为，让用户知道发生了什么
		result.Steps[0].Message += "；已改用本地已有镜像启动"
		result.Output += "\n提示: " + result.Steps[0].Message
	}

	// 容器没重建时给个结论。区分两种情况，避免误导：
	//   1. 确实有镜像拉取失败 → 可能拉不到新版本
	//   2. 镜像都拉取成功（或只有本地构建镜像）→ 本来就是最新，属于正常结果
	if upStep.Status == "success" && isAllUnchanged(upStep.Message) {
		if len(realPullFailures(outcomes, buildable)) > 0 {
			result.Steps = append(result.Steps, JobStep{
				Stage:   "result",
				Status:  "warning",
				Message: "有镜像拉取失败，容器未重建，可能仍在运行旧版本；请检查镜像地址与仓库权限",
			})
		} else {
			result.Steps = append(result.Steps, JobStep{
				Stage:   "result",
				Status:  "success",
				Message: "镜像已是最新，容器无需重建",
			})
		}
	}
	return e.attachServiceSnapshot(ctx, job, result)
}

// realPullFailures 返回真正拉取失败的镜像，排除声明了 build 的本地构建镜像。
func realPullFailures(outcomes []imagePullOutcome, buildable map[string]bool) []string {
	var failed []string
	for _, item := range outcomes {
		if item.Status == "failed" && !buildable[item.Image] {
			failed = append(failed, item.Image)
		}
	}
	return failed
}

// isAllUnchanged 判断 up 摘要是否只有「无变化」的容器。
func isAllUnchanged(message string) bool {
	return strings.Contains(message, "无变化") && !strings.Contains(message, "已重建") &&
		!strings.Contains(message, "已创建") && !strings.Contains(message, "已启动")
}

// writeResult 落盘结果并清理过期记录。
func (e *Executor) writeResult(result JobResult) error {
	if !isSafeID(result.ID) {
		return errors.New("非法请求 ID")
	}
	if err := atomicJSON(filepath.Join(e.resultDir, result.ID+".json"), result); err != nil {
		return err
	}
	e.pruneResults()
	return nil
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
		id := strings.TrimSuffix(old.name, ".json")
		// Never resurrect an active lifecycle by pruning its final result.
		if _, err := os.Stat(filepath.Join(e.queueDir, old.name+claimSuffix)); err == nil {
			continue
		}
		if err := os.Remove(e.lifecyclePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
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
