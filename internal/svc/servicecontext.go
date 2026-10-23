package svc

import (
	"path/filepath"
	"sync"

	"github.com/docker/docker/client"
	"github.com/onlyLTY/dockerCopilot/internal/config"
	"github.com/onlyLTY/dockerCopilot/internal/module"
	"github.com/onlyLTY/dockerCopilot/internal/utiles/composefile"
	"github.com/onlyLTY/dockerCopilot/internal/utiles/releasewatch"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
)

type ServiceContext struct {
	Config                     config.Config
	CookieCheckMiddleware      rest.Middleware
	Jwtuuid                    string
	BearerTokenCheckMiddleware rest.Middleware
	JwtSecret                  string
	PortainerJwt               string
	HubImageInfo               *module.ImageUpdateData
	IndexCheckMiddleware       rest.Middleware
	ProgressStore              ProgressStoreType
	DockerClient               *client.Client
	ContainerStatsCache        *module.ContainerStatsCache
	UpdateRecords              *module.UpdateRecordStore
	ComposeStore               *composefile.Store
	ComposeExecutor            *composefile.Executor
	ReleaseStore               *releasewatch.Store
	ReleaseWatcher             *releasewatch.Watcher
	mu                         sync.Mutex
}

type TaskProgress struct {
	TaskID     string
	Percentage int
	Message    string
	Name       string
	DetailMsg  string
	IsDone     bool
}

type ProgressStoreType map[string]TaskProgress

func NewServiceContext(c config.Config) *ServiceContext {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logx.Errorf("Unable to create docker client: %s", err)
	}

	releaseStore := releasewatch.NewStore(c.ReleaseWatchDataDir())
	checker := releasewatch.NewChecker(releaseStore, c.ReleaseWatch.GitHubToken)
	watcher := releasewatch.NewWatcher(checker, c.CheckInterval())

	// 执行器需要 docker client 精确识别容器归属，避免误删其他 compose 文件的容器
	composeExecutor := composefile.NewExecutor(c.ComposeDir())
	if cli != nil {
		composeExecutor = composeExecutor.WithDockerClient(cli)
	}

	return &ServiceContext{
		Config:              c,
		HubImageInfo:        module.NewImageCheck(),
		ProgressStore:       make(ProgressStoreType),
		DockerClient:        cli,
		ContainerStatsCache: module.NewContainerStatsCache(),
		UpdateRecords:       module.NewUpdateRecordStore(updateRecordPath(c)),
		ComposeStore:        composefile.NewStore(c.ComposeDir()),
		ComposeExecutor:     composeExecutor,
		ReleaseStore:        releaseStore,
		ReleaseWatcher:      watcher,
	}
}

func (ctx *ServiceContext) UpdateProgress(taskID string, progress TaskProgress) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	ctx.ProgressStore[taskID] = progress
}

func (ctx *ServiceContext) GetProgress(taskID string) (TaskProgress, bool) {
	ctx.mu.Lock()
	defer ctx.mu.Unlock()
	progress, ok := ctx.ProgressStore[taskID]
	return progress, ok
}

// updateRecordPath 更新记录落在与关注列表相同的数据目录，随容器卷持久化。
func updateRecordPath(c config.Config) string {
	return filepath.Join(c.ReleaseWatchDataDir(), "container-updates.json")
}
