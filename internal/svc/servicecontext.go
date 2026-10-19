package svc

import (
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

	return &ServiceContext{
		Config:          c,
		HubImageInfo:    module.NewImageCheck(),
		ProgressStore:   make(ProgressStoreType),
		DockerClient:    cli,
		ComposeStore:    composefile.NewStore(c.ComposeDir()),
		ComposeExecutor: composefile.NewExecutor(c.ComposeDir()),
		ReleaseStore:    releaseStore,
		ReleaseWatcher:  watcher,
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
