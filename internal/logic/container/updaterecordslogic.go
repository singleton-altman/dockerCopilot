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
	Phase           string `json:"phase"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
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

// List 返回最近的更新记录（含进行中与历史）。
func (l *UpdatesLogic) List() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	records := l.svcCtx.UpdateRecords.List()
	views := make([]UpdateRecordView, 0, len(records))
	for _, rec := range records {
		views = append(views, UpdateRecordView{
			ID:              rec.ID,
			ContainerName:   rec.ContainerName,
			OriginalImage:   rec.OriginalImage,
			ImageNameAndTag: rec.ImageNameAndTag,
			Phase:           rec.Phase,
			Status:          rec.Status,
			Error:           rec.Error,
			CanCancel:       rec.Status == module.StatusRunning,
			CreatedAt:       rec.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt:       rec.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
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

// Cancel 中断更新。只有镜像拉取阶段可中断（容器未被改动，直接停止即可）；
// 进入容器变更阶段后不可中断，更新会执行到底，失败时自动恢复旧容器。
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
	switch rec.Phase {
	case module.PhasePulling:
		if rec.Status != module.StatusRunning {
			resp.Code = 400
			resp.Msg = "更新已结束"
			resp.Data = map[string]interface{}{}
			return resp, nil
		}
		l.svcCtx.UpdateRecords.RequestCancel(name)
		resp.Code = 0
		resp.Msg = "已请求中断，正在停止镜像拉取"
		resp.Data = map[string]string{"containerName": name}
		return resp, nil
	case module.PhaseCanceled:
		resp.Code = 400
		resp.Msg = "该更新已被中断"
		resp.Data = map[string]interface{}{}
		return resp, nil
	default:
		// stopped/renamed/created/started/done：容器变更已开始，不可中断
		resp.Code = 400
		resp.Msg = "更新已进入容器变更阶段，无法中断，请等待完成"
		resp.Data = map[string]interface{}{}
		return resp, nil
	}
}
