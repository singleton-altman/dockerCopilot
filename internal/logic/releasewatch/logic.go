package releasewatch

import (
	"context"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type Logic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewLogic(ctx context.Context, svcCtx *svc.ServiceContext) *Logic {
	return &Logic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// List 返回关注列表及每个仓库的最近状态
func (l *Logic) List() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	repos := l.svcCtx.ReleaseStore.Repos()

	items := make([]map[string]interface{}, 0, len(repos))
	for _, repo := range repos {
		state := l.svcCtx.ReleaseStore.State(repo)
		item := map[string]interface{}{
			"repo":        repo,
			"lastTag":     state.LastTag,
			"lastCheckAt": state.LastCheckAt,
		}
		if state.LastError != "" {
			item["lastError"] = state.LastError
		}
		items = append(items, item)
	}

	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"repos": items}
	return resp, nil
}

// Add 添加关注仓库
func (l *Logic) Add(req *types.ReleaseRepoReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ReleaseStore.Add(req.Repo); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{}
	return resp, nil
}

// Remove 移除关注仓库
func (l *Logic) Remove(req *types.ReleaseRepoReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ReleaseStore.Remove(req.Repo); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{}
	return resp, nil
}

// Check 立即检查一次，并更新去重基线。
func (l *Logic) Check() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	results := l.svcCtx.ReleaseWatcher.RunOnce()
	lastRunAt, _ := l.svcCtx.ReleaseWatcher.Status()

	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{
		"results":   results,
		"checkedAt": lastRunAt,
	}
	return resp, nil
}
