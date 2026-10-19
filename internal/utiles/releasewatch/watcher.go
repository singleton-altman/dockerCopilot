package releasewatch

import (
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
)

// Watcher 定时检查关注仓库的发布状态。
// 这里只负责发现和记录，不承担推送职责。
type Watcher struct {
	checker     *Checker
	interval    time.Duration
	mu          sync.Mutex
	running     bool
	lastRunAt   time.Time
	lastResults []CheckResult
	stopCh      chan struct{}
}

func NewWatcher(checker *Checker, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Hour
	}
	return &Watcher{
		checker:  checker,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动后台检查。只应在进程启动时调用一次。
func (w *Watcher) Start() {
	go func() {
		// 启动后先跑一次，便于尽快建立基线。
		w.RunOnce()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.RunOnce()
			case <-w.stopCh:
				return
			}
		}
	}()
	logx.Infof("仓库发布监控已启动，检查间隔 %s", w.interval)
}

func (w *Watcher) Stop() {
	close(w.stopCh)
}

// RunOnce 执行一次检查并更新去重基线。
func (w *Watcher) RunOnce() []CheckResult {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return w.lastResults
	}
	w.running = true
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
	}()

	results := w.checker.CheckAll()
	for _, result := range results {
		switch {
		case result.Error != "":
			logx.Errorf("检查 %s 发布失败: %s", result.Repo, result.Error)
		case result.IsNew:
			logx.Infof("%s 有新版本 %s", result.Repo, result.Release.Tag)
		}
	}

	w.mu.Lock()
	w.lastRunAt = time.Now()
	w.lastResults = results
	w.mu.Unlock()

	return results
}

// Status 返回最近一次检查时间和结果
func (w *Watcher) Status() (time.Time, []CheckResult) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastRunAt, w.lastResults
}
