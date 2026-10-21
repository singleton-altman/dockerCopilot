package container

import (
	"context"
	"os"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/onlyLTY/dockerCopilot/internal/utiles"
	"github.com/zeromicro/go-zero/core/logx"
)

type UpdateLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateLogic {
	return &UpdateLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *UpdateLogic) Update(req *types.ContainerUpdateReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}

	// 先建记录：并发校验与旧容器配置快照都在这里完成，
	// 失败时直接返回给客户端，不产生后台任务。
	rec, err := utiles.BeginUpdate(l.svcCtx, req.Id, req.ContainerName, req.ImageNameAndTag)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, err
	}

	delOldContainer := os.Getenv("DelOldContainer") != "false"
	go func() {
		defer func() {
			if r := recover(); r != nil {
				l.Errorf("Recovered from panic in UpdateContainer: %v", r)
			}
		}()
		if err := utiles.UpdateContainer(l.svcCtx, rec, delOldContainer); err != nil {
			l.Errorf("Error in UpdateContainer: %v", err)
		}
	}()

	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]string{"taskID": rec.ID, "recordID": rec.ID}
	return resp, nil
}
