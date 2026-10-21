package container

import (
	"context"

	"github.com/onlyLTY/dockerCopilot/internal/module"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/onlyLTY/dockerCopilot/internal/utiles"
	"github.com/zeromicro/go-zero/core/logx"
)

// UpdateRecordView 是返回给客户端的更新记录视图。
type UpdateRecordView struct {
	ID              string `json:"id"`
	ContainerName   string `json:"containerName"`
	OriginalImage   string `json:"originalImage"`
	ImageNameAndTag string `json:"imageNameAndTag"`
	RenamedTo       string `json:"renamedTo,omitempty"`
	Phase           string `json:"phase"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	CanRollback     bool   `json:"canRollback"`
	CanCancel       bool   `json:"canCancel"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type UpdatesLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdatesLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdatesLogic {
	return &UpdatesLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

// List 返回最近的更新记录。顺带清理过期旧容器，无需常驻后台任务。
func (l *UpdatesLogic) List() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	utiles.CleanupExpired(l.svcCtx)

	records := l.svcCtx.UpdateRecords.List()
	views := make([]UpdateRecordView, 0, len(records))
	for _, rec := range records {
		views = append(views, toRecordView(rec))
	}
	resp.Code = 0
	resp.Msg = "success"
	resp.Data = views
	return resp, nil
}

type CancelLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCancelLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CancelLogic {
	return &CancelLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

// Cancel 中断正在进行的更新。更新协程在下一个阶段边界停止并自动回撤；
// 已完成的更新不允许取消（应使用回撤）。
func (l *CancelLogic) Cancel(req *types.IdReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	name := utiles.ResolveRecordName(l.svcCtx, req.Id)
	rec, ok := l.svcCtx.UpdateRecords.Snapshot(name)
	if !ok {
		resp.Code = 404
		resp.Msg = "未找到该容器的更新记录"
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	switch rec.Status {
	case module.StatusRunning:
		l.svcCtx.UpdateRecords.RequestCancel(name)
		// 更新协程可能已经结束，此时直接回撤，避免请求落空。
		if latest, ok := l.svcCtx.UpdateRecords.Snapshot(name); ok &&
			latest.Status != module.StatusRunning {
			_ = utiles.Rollback(l.svcCtx, name, module.StatusCanceled, "用户中断更新")
		}
		resp.Code = 0
		resp.Msg = "已请求中断，正在回撤"
		resp.Data = map[string]string{"containerName": name}
		return resp, nil
	case module.StatusSucceeded, module.StatusFailed:
		resp.Code = 400
		resp.Msg = "更新已结束，请使用回撤"
		resp.Data = map[string]interface{}{}
		return resp, nil
	case module.StatusRolledBack, module.StatusCanceled:
		resp.Code = 400
		resp.Msg = "该更新已回撤或中断，无需再次取消"
		resp.Data = map[string]interface{}{}
		return resp, nil
	default:
		resp.Code = 400
		resp.Msg = "当前状态不可中断: " + rec.Status
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
}

type RollbackLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRollbackLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RollbackLogic {
	return &RollbackLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

// Rollback 恢复到更新前的容器与镜像，可处理进行中、成功、失败三种状态。
func (l *RollbackLogic) Rollback(req *types.IdReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	name := utiles.ResolveRecordName(l.svcCtx, req.Id)
	if _, ok := l.svcCtx.UpdateRecords.Snapshot(name); !ok {
		resp.Code = 404
		resp.Msg = "未找到该容器的更新记录"
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	finalStatus := module.StatusRolledBack
	if err := utiles.Rollback(l.svcCtx, name, finalStatus, "用户手动回撤"); err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
	resp.Code = 0
	resp.Msg = "已回撤到更新前版本"
	resp.Data = map[string]string{"containerName": name}
	return resp, nil
}

func toRecordView(rec module.ContainerUpdateRecord) UpdateRecordView {
	return UpdateRecordView{
		ID:              rec.ID,
		ContainerName:   rec.ContainerName,
		OriginalImage:   rec.OriginalImage,
		ImageNameAndTag: rec.ImageNameAndTag,
		RenamedTo:       rec.RenamedTo,
		Phase:           rec.Phase,
		Status:          rec.Status,
		Error:           rec.Error,
		CanRollback:     rec.CanRollback(),
		CanCancel:       rec.Status == module.StatusRunning,
		CreatedAt:       rec.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:       rec.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
