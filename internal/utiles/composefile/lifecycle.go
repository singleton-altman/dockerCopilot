package composefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const lifecycleDirName = ".jobs"
const builtinOwnerKind = "dockercopilot-builtin-v1"

type JobStatus string

const (
	StatusQueued      JobStatus = "queued"
	StatusRunning     JobStatus = "running"
	StatusSucceeded   JobStatus = "succeeded"
	StatusFailed      JobStatus = "failed"
	StatusInterrupted JobStatus = "interrupted"
	StatusNotFound    JobStatus = "not_found"
)

// JobView is the additive public lifecycle contract. All timestamps are RFC3339.
type JobView struct {
	ID        string     `json:"id"`
	Filename  string     `json:"filename"`
	Action    Action     `json:"action"`
	Status    JobStatus  `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Result    *JobResult `json:"result,omitempty"`
	// ConfigSha256 投递时文件内容的 SHA-256（hex 小写）；旧任务/旧 watcher 可缺省。
	ConfigSha256 string `json:"configSha256,omitempty"`
}

type jobOwner struct {
	Kind     string `json:"kind"`
	Instance string `json:"instance"`
	Process  string `json:"process"`
}

type lifecycleRecord struct {
	JobView
	Owner jobOwner `json:"owner"`
}

func (e *Executor) lifecyclePath(id string) string {
	return filepath.Join(e.workDir, lifecycleDirName, id+".json")
}

// atomicJSON never exposes partial JSON. Sync both file and directory before
// reporting success, so a failed write never authorizes deleting a claim.
func atomicJSON(path string, value any) error {
	return atomicJSONMode(path, value, 0o600)
}

func atomicJSONMode(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".job-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readJSON(path string, value any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, value)
}

// startup is once per executor, never called by a query. The directory lock
// prevents a second built-in executor from interrupting a still-live process.
func (e *Executor) startup() error {
	e.startupOnce.Do(func() { e.startupErr = e.initializeOwner() })
	return e.startupErr
}

func (e *Executor) initializeOwner() error {
	dir := filepath.Join(e.workDir, lifecycleDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".executor.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return fmt.Errorf("built-in compose executor already owned or lock unavailable: %w", err)
	}
	e.ownerLock = lock
	ownerPath := filepath.Join(dir, ".executor-owner.json")
	var previous jobOwner
	if err := readJSON(ownerPath, &previous); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			lock.Close()
			return err
		}
		previous = jobOwner{Kind: builtinOwnerKind, Instance: uuid.NewString()}
	}
	if previous.Kind != builtinOwnerKind || previous.Instance == "" {
		lock.Close()
		return errors.New("unrecognized built-in executor owner")
	}
	e.owner = jobOwner{Kind: builtinOwnerKind, Instance: previous.Instance, Process: uuid.NewString()}
	if err := atomicJSON(ownerPath, e.owner); err != nil {
		lock.Close()
		return err
	}
	if err := e.recoverOwned(); err != nil {
		lock.Close()
		return err
	}
	return nil
}

func (e *Executor) ownsOld(owner jobOwner) bool {
	return owner.Kind == builtinOwnerKind && owner.Instance == e.owner.Instance &&
		owner.Process != "" && owner.Process != e.owner.Process
}

// recoverOwned does not replay anything. Unknown/legacy/external claims are
// deliberately untouched, even when their payload looks like a normal job.
func (e *Executor) recoverOwned() error {
	ids := map[string]bool{}
	for _, dir := range []string{e.queueDir, filepath.Join(e.workDir, lifecycleDirName)} {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if dir == e.queueDir {
				if !strings.HasSuffix(name, ".json"+claimSuffix) {
					continue
				}
				name = strings.TrimSuffix(name, claimSuffix)
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if isSafeID(id) && !strings.HasPrefix(id, ".") {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		var record lifecycleRecord
		recordErr := readJSON(e.lifecyclePath(id), &record)
		var job Job
		claimPath := filepath.Join(e.queueDir, id+".json"+claimSuffix)
		claimErr := readJSON(claimPath, &job)
		ownedRecord := recordErr == nil && record.ID == id && record.Status == StatusRunning && e.ownsOld(record.Owner)
		ownedClaim := claimErr == nil && job.ID == id && job.Owner != nil && e.ownsOld(*job.Owner)
		// An explicit foreign claim always wins over stale lifecycle metadata.
		if claimErr == nil && !ownedClaim {
			continue
		}
		if !ownedRecord && !ownedClaim {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.queueDir, id+".json")); err == nil {
			continue
		}
		if result, done, err := e.Result(id); err != nil {
			return err
		} else if done {
			_ = result
			_ = os.Remove(e.lifecyclePath(id))
			if ownedClaim {
				_ = os.Remove(claimPath)
			}
			continue
		}
		if !ownedRecord {
			record.JobView = viewFromJob(job, StatusRunning)
		}
		result := interruptedResult("服务端执行进程已中断，执行结果不确定；请核验实际容器和镜像状态，勿自动重试")
		result.ID, result.Filename, result.Action = id, record.Filename, record.Action
		result.CreatedAt, result.StartedAt = &record.CreatedAt, record.StartedAt
		// 中断记录保留投递指纹，客户端才能继续核对“部署后文件是否被修改”。
		if result.ConfigSha256 == "" {
			result.ConfigSha256 = record.ConfigSha256
		}
		if result.ConfigSha256 == "" && ownedClaim {
			result.ConfigSha256 = job.ConfigSha256
		}
		if err := e.writeResult(result); err != nil {
			return err
		}
		_ = os.Remove(e.lifecyclePath(id))
		if ownedClaim {
			_ = os.Remove(claimPath)
		}
	}
	return nil
}

func interruptedResult(message string) JobResult {
	return JobResult{Success: false, Status: StatusInterrupted, Output: message, FinishedAt: time.Now()}
}

func viewFromJob(job Job, status JobStatus) JobView {
	return JobView{ID: job.ID, Filename: job.Filename, Action: job.Action, Status: status,
		CreatedAt: job.CreatedAt, ConfigSha256: job.ConfigSha256}
}

func resultStatus(result JobResult) JobStatus {
	if result.Status == StatusInterrupted {
		return StatusInterrupted
	}
	if result.Success {
		return StatusSucceeded
	}
	return StatusFailed
}

// View reads files only. Transient polling errors cannot mutate/cancel a job.
func (e *Executor) View(id string) (*JobView, error) {
	if !isSafeID(id) {
		return nil, errors.New("非法请求 ID")
	}
	// Retry all sources a bounded number of times across queue -> claim ->
	// lifecycle -> result publication. Queries never perform recovery writes.
	for attempt := 0; attempt < 2; attempt++ {
		view, err := e.readView(id)
		if err != nil || view != nil {
			return view, err
		}
	}
	return nil, nil
}

func (e *Executor) readView(id string) (*JobView, error) {
	if result, done, err := e.Result(id); err != nil {
		return nil, err
	} else if done {
		createdAt := result.FinishedAt // Legacy watchers have no creation timestamp.
		if result.CreatedAt != nil {
			createdAt = *result.CreatedAt
		}
		var record lifecycleRecord
		if readJSON(e.lifecyclePath(id), &record) == nil && record.ID == id {
			createdAt = record.CreatedAt
			if result.StartedAt == nil {
				result.StartedAt = record.StartedAt
			}
		}
		view := &JobView{ID: id, Filename: result.Filename, Action: result.Action, Status: resultStatus(result),
			CreatedAt: createdAt, StartedAt: result.StartedAt, UpdatedAt: &result.FinishedAt, Result: &result,
			ConfigSha256: result.ConfigSha256}
		// 旧 watcher 的结果没有创建时间/指纹，回落到仍存在的队列任务上补齐。
		if result.CreatedAt == nil || result.ConfigSha256 == "" {
			var job Job
			for _, suffix := range []string{".json", ".json" + claimSuffix} {
				if readJSON(filepath.Join(e.queueDir, id+suffix), &job) == nil && job.ID == id {
					if result.CreatedAt == nil {
						view.CreatedAt = job.CreatedAt
					}
					if result.ConfigSha256 == "" {
						view.ConfigSha256 = job.ConfigSha256
					}
					break
				}
			}
		}
		return view, nil
	}
	var record lifecycleRecord
	if err := readJSON(e.lifecyclePath(id), &record); err == nil {
		if record.ID != id {
			return nil, errors.New("生命周期请求 ID 不匹配")
		}
		return &record.JobView, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, candidate := range []struct {
		suffix string
		status JobStatus
	}{{".json", StatusQueued}, {".json" + claimSuffix, StatusRunning}} {
		var job Job
		if err := readJSON(filepath.Join(e.queueDir, id+candidate.suffix), &job); err == nil {
			if job.ID != id {
				return nil, errors.New("队列请求 ID 不匹配")
			}
			view := viewFromJob(job, candidate.status)
			return &view, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, nil
}

// Jobs includes every active job and at most limit final jobs, independently.
func (e *Executor) Jobs(limit int) ([]JobView, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	ids := map[string]bool{}
	for _, dir := range []string{e.queueDir, e.resultDir, filepath.Join(e.workDir, lifecycleDirName)} {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := strings.TrimSuffix(entry.Name(), claimSuffix)
			if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if isSafeID(id) {
				ids[id] = true
			}
		}
	}
	active, final := []JobView{}, []JobView{}
	for id := range ids {
		view, err := e.View(id)
		if err != nil {
			return nil, err
		}
		if view == nil {
			continue
		}
		if view.Status == StatusQueued || view.Status == StatusRunning {
			active = append(active, *view)
		} else {
			final = append(final, *view)
		}
	}
	sortViews(final)
	if len(final) > limit {
		final = final[:limit]
	}
	jobs := append(active, final...)
	sortViews(jobs)
	return jobs, nil
}

func sortViews(jobs []JobView) {
	sort.Slice(jobs, func(i, j int) bool {
		a, b := jobs[i].CreatedAt, jobs[j].CreatedAt
		if jobs[i].UpdatedAt != nil {
			a = *jobs[i].UpdatedAt
		}
		if jobs[j].UpdatedAt != nil {
			b = *jobs[j].UpdatedAt
		}
		if a.Equal(b) {
			return jobs[i].ID > jobs[j].ID
		}
		return a.After(b)
	})
}
