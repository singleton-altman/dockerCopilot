package composefile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func releaseOwner(t *testing.T, ex *Executor) {
	t.Helper()
	if ex.ownerLock != nil {
		if err := ex.ownerLock.Close(); err != nil {
			t.Fatal(err)
		}
		ex.ownerLock = nil
	}
}

func requireView(t *testing.T, ex *Executor, id string, status JobStatus) *JobView {
	t.Helper()
	view, err := ex.View(id)
	if err != nil || view == nil || view.Status != status {
		t.Fatalf("View(%s): want %s, got %+v err=%v", id, status, view, err)
	}
	return view
}

func seedOwnedRunning(t *testing.T, ex *Executor, id string) {
	t.Helper()
	job := Job{ID: id, Filename: "web.yaml", Action: ActionDeploy, CreatedAt: time.Now(), Owner: &ex.owner}
	started := time.Now()
	record := lifecycleRecord{JobView: viewFromJob(job, StatusRunning), Owner: ex.owner}
	record.StartedAt, record.UpdatedAt = &started, &started
	if err := atomicJSON(filepath.Join(ex.queueDir, id+".json"+claimSuffix), job); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(ex.lifecyclePath(id), record); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleQueuedRunningFinalAndReadOnlyPolling(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	requireView(t, ex, id, StatusQueued)
	entered, finish := make(chan struct{}), make(chan struct{})
	ex.runner = func(ctx context.Context, job Job) JobResult {
		close(entered)
		<-finish
		return JobResult{Success: true, Output: "done"}
	}
	drained := make(chan struct{})
	go func() { ex.drainOnce(context.Background()); close(drained) }()
	<-entered
	view := requireView(t, ex, id, StatusRunning)
	if view.StartedAt == nil || view.UpdatedAt == nil || view.Result != nil {
		t.Fatalf("bad running view: %+v", view)
	}
	// A single corrupted response source is a query error, not task cancellation.
	if err := os.WriteFile(filepath.Join(ex.resultDir, id+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.View(id); err == nil {
		t.Fatal("expected transient read failure")
	}
	if err := os.Remove(filepath.Join(ex.resultDir, id+".json")); err != nil {
		t.Fatal(err)
	}
	requireView(t, ex, id, StatusRunning)
	close(finish)
	<-drained
	view = requireView(t, ex, id, StatusSucceeded)
	if view.Result == nil || !view.Result.Success || view.StartedAt == nil {
		t.Fatalf("bad final view: %+v", view)
	}
	for _, path := range []string{ex.lifecyclePath(id), filepath.Join(ex.queueDir, id+".json"+claimSuffix)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected cleanup %s: %v", path, err)
		}
	}
	releaseOwner(t, ex)
}

func TestRestartOnlyInterruptsExplicitOldOwnerOnce(t *testing.T) {
	ex := newExecutor(t)
	if err := ex.startup(); err != nil {
		t.Fatal(err)
	}
	seedOwnedRunning(t, ex, "owned")
	// A result already committed before a crash takes precedence over running.
	seedOwnedRunning(t, ex, "committed")
	if err := ex.writeResult(JobResult{ID: "committed", Success: true, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Legacy and foreign owners must not be interrupted or replayed.
	for _, id := range []string{"legacy", "foreign"} {
		job := Job{ID: id, Filename: "web.yaml", Action: ActionDeploy, CreatedAt: time.Now()}
		if id == "foreign" {
			job.Owner = &jobOwner{Kind: builtinOwnerKind, Instance: "different", Process: "old"}
		}
		if err := atomicJSON(filepath.Join(ex.queueDir, id+".json"+claimSuffix), job); err != nil {
			t.Fatal(err)
		}
	}
	// Valid queued requests survive a restart and are not called interrupted.
	queued, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	releaseOwner(t, ex)
	restarted := NewExecutor(ex.workDir)
	if err := restarted.startup(); err != nil {
		t.Fatal(err)
	}
	defer releaseOwner(t, restarted)
	view := requireView(t, restarted, "owned", StatusInterrupted)
	if view.Result == nil || view.Result.Success || !strings.Contains(view.Result.Output, "不确定") || !strings.Contains(view.Result.Output, "核验") {
		t.Fatalf("interruption must be explicitly uncertain: %+v", view)
	}
	requireView(t, restarted, "committed", StatusSucceeded)
	requireView(t, restarted, queued, StatusQueued)
	for _, id := range []string{"legacy", "foreign"} {
		requireView(t, restarted, id, StatusRunning)
		if _, done, err := restarted.Result(id); err != nil || done {
			t.Fatalf("foreign claim changed: %s", id)
		}
		if _, err := os.Stat(filepath.Join(ex.queueDir, id+".json"+claimSuffix)); err != nil {
			t.Fatal(err)
		}
	}
	// Once started, no query/startup may recover a newly appearing old record.
	seedOwnedRunning(t, ex, "late-old")
	if err := restarted.startup(); err != nil {
		t.Fatal(err)
	}
	requireView(t, restarted, "late-old", StatusRunning)
}

func TestResultWriteFailurePreservesClaimAndLifecycleWithoutReplay(t *testing.T) {
	ex := newExecutor(t)
	calls := 0
	ex.runner = func(ctx context.Context, job Job) JobResult {
		calls++
		return JobResult{Success: true, Output: "business may have succeeded"}
	}
	id, err := ex.Submit("web.yaml", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic write error, without chmod assumptions or real devices.
	if err := os.Remove(ex.resultDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ex.resultDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex.drainOnce(context.Background())
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	for _, path := range []string{ex.lifecyclePath(id), filepath.Join(ex.queueDir, id+".json"+claimSuffix)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("recovery evidence missing: %s: %v", path, err)
		}
	}
	ex.drainOnce(context.Background())
	if calls != 1 {
		t.Fatal("must not retry a mutation")
	}
	if err := os.Remove(ex.resultDir); err != nil {
		t.Fatal(err)
	}
	requireView(t, ex, id, StatusRunning)
	releaseOwner(t, ex)
	restarted := NewExecutor(ex.workDir)
	if err := restarted.startup(); err != nil {
		t.Fatal(err)
	}
	defer releaseOwner(t, restarted)
	requireView(t, restarted, id, StatusInterrupted)
}

func TestRecoveryFailedResultWriteKeepsEvidence(t *testing.T) {
	ex := newExecutor(t)
	if err := ex.startup(); err != nil {
		t.Fatal(err)
	}
	seedOwnedRunning(t, ex, "old")
	if err := os.WriteFile(ex.resultDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseOwner(t, ex)
	restarted := NewExecutor(ex.workDir)
	if err := restarted.startup(); err == nil {
		t.Fatal("expected recovery failure")
	}
	if _, err := os.Stat(ex.lifecyclePath("old")); err != nil {
		t.Fatal("lost lifecycle", err)
	}
	if _, err := os.Stat(filepath.Join(ex.queueDir, "old.json.claim")); err != nil {
		t.Fatal("lost claim", err)
	}
}

func TestLegacyWatcherViewsAndActiveHistoryNeverTruncated(t *testing.T) {
	ex := newExecutor(t)
	queued, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	running, err := ex.Submit("web.yaml", ActionRestart)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(ex.queueDir, running+".json"), filepath.Join(ex.queueDir, running+".json.claim")); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Hour)
	for i := 0; i < 30; i++ {
		result := JobResult{ID: "legacy-" + strconv.Itoa(i), Filename: "web.yaml", Action: ActionUp, Success: i%2 == 0, FinishedAt: base.Add(time.Duration(i) * time.Second)}
		if err := atomicJSON(filepath.Join(ex.resultDir, result.ID+".json"), result); err != nil {
			t.Fatal(err)
		}
	}
	requireView(t, ex, "legacy-0", StatusSucceeded)
	view := requireView(t, ex, "legacy-1", StatusFailed)
	if !view.CreatedAt.Equal(base.Add(time.Second)) {
		t.Fatal("legacy timestamp fallback missing")
	}
	jobs, err := ex.Jobs(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 22 {
		t.Fatalf("want 20 final + 2 active, got %d", len(jobs))
	}
	found := map[string]bool{}
	for i, job := range jobs {
		found[job.ID] = true
		if i > 0 {
			previous, current := jobs[i-1].CreatedAt, job.CreatedAt
			if jobs[i-1].UpdatedAt != nil {
				previous = *jobs[i-1].UpdatedAt
			}
			if job.UpdatedAt != nil {
				current = *job.UpdatedAt
			}
			if current.After(previous) {
				t.Fatal("history not descending")
			}
		}
	}
	if !found[queued] || !found[running] {
		t.Fatal("active jobs crowded out by finals")
	}
	if unknown, err := ex.View("unknown"); err != nil || unknown != nil {
		t.Fatalf("unknown must not succeed: %+v %v", unknown, err)
	}
}

func TestBuiltinLockAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("COMPOSE_LOCK_TEST_DIR"); dir != "" {
		ex := NewExecutor(dir)
		if err := ex.startup(); err == nil {
			releaseOwner(t, ex)
			t.Fatal("acquired live process lock")
		}
		return
	}
	ex := newExecutor(t)
	if err := ex.startup(); err != nil {
		t.Fatal(err)
	}
	defer releaseOwner(t, ex)
	seedOwnedRunning(t, ex, "live")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBuiltinLockAcrossProcesses$")
	cmd.Env = append(os.Environ(), "COMPOSE_LOCK_TEST_DIR="+ex.workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child lock check: %v\n%s", err, output)
	}
	requireView(t, ex, "live", StatusRunning)
}

func TestConcurrentConsumersExecuteAtMostOnce(t *testing.T) {
	ex := newExecutor(t)
	calls := 0
	ex.runner = func(context.Context, Job) JobResult { calls++; return JobResult{Success: true} }
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() { ex.claimAndRun(context.Background(), id) })
	}
	wg.Wait()
	defer releaseOwner(t, ex)
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	requireView(t, ex, id, StatusSucceeded)
}

func TestSubmitRejectsNonYAMLAndCountsClaims(t *testing.T) {
	ex := newExecutor(t)
	for _, name := range []string{".env", ".env.production", "a.txt", "../a.yaml", "a/b.yml"} {
		if _, err := ex.Submit(name, ActionDeploy); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	ex.maxPending = 1
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(ex.queueDir, id+".json"), filepath.Join(ex.queueDir, id+".json.claim")); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Submit("web.yaml", ActionUp); err == nil {
		t.Fatal("running claims must count toward pending limit")
	}
}

func TestCancellationDoesNotClaimOrStartDeployUp(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex.runner = func(context.Context, Job) JobResult { t.Fatal("cancelled queue must not run"); return JobResult{} }
	ex.drainOnce(ctx)
	requireView(t, ex, id, StatusQueued)
	// A pre-cancelled deploy must not execute either phase, even with a CLI.
	bin := filepath.Join(t.TempDir(), "compose")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ex.composeCmd = []string{bin}
	result := ex.runJob(ctx, Job{Filename: "web.yaml", Action: ActionDeploy})
	if result.Status != StatusInterrupted || result.Success || len(result.Steps) != 2 || result.Steps[1].Status != "skipped" {
		t.Fatalf("cancelled deploy crossed phase boundary: %+v", result)
	}
}

func TestQueueFileRetainsLegacyWatcherReadPermissions(t *testing.T) {
	ex := newExecutor(t)
	id, err := ex.Submit("web.yaml", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(ex.queueDir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("queue mode=%o", info.Mode().Perm())
	}
}

func TestPollingDuringClaimAndFinalPublicationNeverLosesJob(t *testing.T) {
	ex := newExecutor(t)
	ex.runner = func(context.Context, Job) JobResult { return JobResult{Success: true} }
	defer releaseOwner(t, ex)
	for iteration := 0; iteration < 20; iteration++ {
		id, err := ex.Submit("web.yaml", ActionDeploy)
		if err != nil {
			t.Fatal(err)
		}
		var readers sync.WaitGroup
		errorsSeen := make(chan string, 8)
		for reader := 0; reader < 8; reader++ {
			readers.Go(func() {
				for poll := 0; poll < 30; poll++ {
					view, err := ex.View(id)
					if err != nil || view == nil {
						errorsSeen <- fmt.Sprintf("missing during transition: %v", err)
						return
					}
				}
			})
		}
		ex.claimAndRun(context.Background(), id)
		readers.Wait()
		close(errorsSeen)
		for message := range errorsSeen {
			t.Error(message)
		}
		requireView(t, ex, id, StatusSucceeded)
	}
}

func TestPruningFinalsCleansLifecycleButPreservesActive(t *testing.T) {
	ex := newExecutor(t)
	if err := ex.startup(); err != nil {
		t.Fatal(err)
	}
	defer releaseOwner(t, ex)
	seedOwnedRunning(t, ex, "active")
	for i := 0; i < resultKeep+5; i++ {
		id := "final-" + strconv.Itoa(i)
		record := lifecycleRecord{JobView: JobView{ID: id, Status: StatusRunning}, Owner: ex.owner}
		if err := atomicJSON(ex.lifecyclePath(id), record); err != nil {
			t.Fatal(err)
		}
		if err := ex.writeResult(JobResult{ID: id, Success: true, FinishedAt: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(ex.lifecyclePath("final-0")); !os.IsNotExist(err) {
		t.Fatal("pruned final lifecycle remains", err)
	}
	requireView(t, ex, "active", StatusRunning)
}

func TestDeployCancellationDuringPullNeverInvokesUp(t *testing.T) {
	ex := newExecutor(t)
	dir := t.TempDir()
	bin, callsPath := filepath.Join(dir, "compose"), filepath.Join(dir, "calls")
	// Test-only CLI; exec avoids leaving a child with an inherited output pipe.
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + callsPath + "\"\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ex.composeCmd = []string{bin}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan JobResult, 1)
	go func() { finished <- ex.runJob(ctx, Job{Filename: "web.yaml", Action: ActionDeploy}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(callsPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake pull did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case result := <-finished:
		if result.Status != StatusInterrupted || result.Success || len(result.Steps) != 2 || result.Steps[1].Status != "skipped" {
			t.Fatalf("bad interrupted deploy: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled pull did not return")
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "up -d") || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 1 {
		t.Fatalf("deploy crossed boundary: %s", calls)
	}
}

func TestCancellationDuringRunnerReportsUncertainNotBusinessFailure(t *testing.T) {
	ex := newExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	id, err := ex.Submit("web.yaml", ActionDeploy)
	if err != nil {
		t.Fatal(err)
	}
	ex.runner = func(context.Context, Job) JobResult { cancel(); return JobResult{Success: true} }
	ex.drainOnce(ctx)
	defer releaseOwner(t, ex)
	view := requireView(t, ex, id, StatusInterrupted)
	if view.Result.Success || !strings.Contains(view.Result.Output, "不确定") {
		t.Fatalf("bad interrupted result: %+v", view.Result)
	}
}

// 取消落在 runner 成功返回之后时，任务降级为 interrupted；
// 与 services 口径一致：服务快照与 ps 结论必须一并清除。
func TestCancellationAfterRunnerDropsServiceSnapshot(t *testing.T) {
	ex := newExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	id, err := ex.Submit("web.yaml", ActionUp)
	if err != nil {
		t.Fatal(err)
	}
	ex.runner = func(context.Context, Job) JobResult {
		cancel()
		return JobResult{Success: true, Status: StatusSucceeded, Output: "ran",
			Steps: []JobStep{{Stage: "up", Status: "success", Message: "ok"},
				{Stage: "ps", Status: "success", Message: "1 个服务全部运行中"}},
			Services: []ServiceState{{Name: "web", State: "running"}}}
	}
	ex.drainOnce(ctx)
	defer releaseOwner(t, ex)
	view := requireView(t, ex, id, StatusInterrupted)
	if view.Result.Services != nil {
		t.Fatalf("interrupted result must drop services: %+v", view.Result.Services)
	}
	for _, step := range view.Result.Steps {
		if step.Stage == "ps" {
			t.Fatalf("interrupted result must drop ps step: %+v", view.Result.Steps)
		}
	}
	if len(view.Result.Steps) == 0 || view.Result.Steps[0].Stage != "up" {
		t.Fatalf("non-ps steps must be preserved: %+v", view.Result.Steps)
	}
}

// 重启中断记录必须保留投递指纹，客户端才能继续核对“部署后文件是否被修改”。
func TestRecoveryInterruptedResultKeepsFingerprint(t *testing.T) {
	ex := newExecutor(t)
	if err := ex.startup(); err != nil {
		t.Fatal(err)
	}
	const sha = "abababababababababababababababababababababababababababababababab"
	job := Job{ID: "owned", Filename: "web.yaml", Action: ActionDeploy, CreatedAt: time.Now(),
		ConfigSha256: sha, Owner: &ex.owner}
	started := time.Now()
	record := lifecycleRecord{JobView: viewFromJob(job, StatusRunning), Owner: ex.owner}
	record.StartedAt, record.UpdatedAt = &started, &started
	if err := atomicJSON(filepath.Join(ex.queueDir, "owned.json"+claimSuffix), job); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(ex.lifecyclePath("owned"), record); err != nil {
		t.Fatal(err)
	}
	releaseOwner(t, ex)
	restarted := NewExecutor(ex.workDir)
	if err := restarted.startup(); err != nil {
		t.Fatal(err)
	}
	defer releaseOwner(t, restarted)
	view := requireView(t, restarted, "owned", StatusInterrupted)
	if view.Result == nil || view.Result.ConfigSha256 != sha {
		t.Fatalf("interrupted result must keep fingerprint: %+v", view.Result)
	}
	if view.ConfigSha256 != sha {
		t.Fatalf("interrupted view must keep fingerprint: %+v", view)
	}
}
