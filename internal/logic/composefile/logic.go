package composefile

import (
	"context"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/onlyLTY/dockerCopilot/internal/utiles/composefile"

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

// List 返回 stacks 目录下的 compose 文件
func (l *Logic) List() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	items, err := l.svcCtx.ComposeStore.List()
	if err != nil {
		resp.Code = 500
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, err
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{
		"dir":   l.svcCtx.ComposeStore.Dir(),
		"files": items,
	}
	return resp, nil
}

// Read 读取单个 compose 文件
func (l *Logic) Read(req *types.ComposeReadReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	content, err := l.svcCtx.ComposeStore.Read(req.Filename)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{
		"filename": req.Filename,
		"content":  content,
	}
	return resp, nil
}

// Save 新建或覆盖 compose 文件
func (l *Logic) Save(req *types.ComposeFileReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	created, err := l.svcCtx.ComposeStore.Save(req.Filename, req.Content)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	if created {
		logx.Infof("compose 文件已创建: %s", req.Filename)
	} else {
		logx.Infof("compose 文件已保存（旧版本已备份）: %s", req.Filename)
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"created": created}
	return resp, nil
}

// Delete 删除 compose 文件
func (l *Logic) Delete(req *types.ComposeDeleteReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ComposeStore.Delete(req.Filename); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	logx.Infof("compose 文件已删除: %s", req.Filename)
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{}
	return resp, nil
}

// Validate 仅校验，不落盘。
// 会注入 stacks 目录的 .env，因此未定义的变量能在这里被发现。
func (l *Logic) Validate(req *types.ComposeFileReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ComposeStore.ValidateContent(req.Filename, req.Content); err != nil {
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

// Deploy 投递执行请求。
// 服务端不会自行执行 compose，由宿主机侧 watcher 消费队列。
func (l *Logic) Deploy(req *types.ComposeDeployReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}

	// 投递前确认文件存在且结构合法，避免让 watcher 执行一份坏配置
	if _, err := l.svcCtx.ComposeStore.Read(req.Filename); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}

	id, err := l.svcCtx.ComposeExecutor.Submit(req.Filename, composefile.Action(req.Action))
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}

	logx.Infof("compose 执行请求已投递: %s %s (job %s)", req.Action, req.Filename, id)
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"jobID": id}
	return resp, nil
}

// Result 查询执行结果
func (l *Logic) Result(req *types.ComposeJobReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	result, done, err := l.svcCtx.ComposeExecutor.Result(req.JobID)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{
		"done":   done,
		"result": result,
	}
	return resp, nil
}

// History 返回最近执行记录
func (l *Logic) History() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	results, err := l.svcCtx.ComposeExecutor.Recent(20)
	if err != nil {
		resp.Code = 500
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"results": results}
	return resp, nil
}

// Versions 返回某个文件的版本历史
func (l *Logic) Versions(req *types.ComposeReadReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	versions, err := l.svcCtx.ComposeStore.Versions(req.Filename)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"versions": versions}
	return resp, nil
}

// Rollback 回滚到指定备份
func (l *Logic) Rollback(req *types.ComposeRollbackReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ComposeStore.Rollback(req.Filename, req.BackupName); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	logx.Infof("compose 文件已回滚: %s <- %s", req.Filename, req.BackupName)
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{}
	return resp, nil
}

// Trash 返回回收站内容
func (l *Logic) Trash() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	items, err := l.svcCtx.ComposeStore.ListTrash()
	if err != nil {
		resp.Code = 500
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"items": items}
	return resp, nil
}

// Restore 从回收站恢复
func (l *Logic) Restore(req *types.ComposeRestoreReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	if err = l.svcCtx.ComposeStore.Restore(req.Name); err != nil {
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
