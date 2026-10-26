package composefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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

// Deploy 投递执行请求，由内置执行器或宿主机 watcher 消费。
func (l *Logic) Deploy(req *types.ComposeDeployReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}

	// 投递前读取文件：确认存在且可读，并据此计算配置指纹。
	// 文件不存在/不可读直接报业务错误，不再产出注定失败的任务。
	content, err := l.svcCtx.ComposeStore.Read(req.Filename)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	sum := sha256.Sum256([]byte(content))

	id, err := l.svcCtx.ComposeExecutor.SubmitWithConfig(req.Filename, composefile.Action(req.Action),
		hex.EncodeToString(sum[:]))
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
	// Submission is already durable. A transient read error must never turn it
	// into a reported failure that could encourage a duplicate POST.
	if job, viewErr := l.svcCtx.ComposeExecutor.View(id); viewErr == nil && job != nil {
		resp.Data.(map[string]interface{})["job"] = job
	}
	return resp, nil
}

// Result 查询执行结果
func (l *Logic) Result(req *types.ComposeJobReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	job, err := l.svcCtx.ComposeExecutor.View(req.JobID)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	data := map[string]interface{}{
		"done":   false,
		"result": composefile.JobResult{},
		"status": composefile.StatusNotFound,
	}
	if job != nil {
		data["job"], data["status"] = job, job.Status
		if job.Result != nil {
			data["done"], data["result"] = true, *job.Result
		}
	}
	resp.Data = data
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
	jobs, err := l.svcCtx.ComposeExecutor.Jobs(20)
	if err != nil {
		resp.Code, resp.Msg, resp.Data = 500, err.Error(), map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{"results": results, "jobs": jobs}
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
