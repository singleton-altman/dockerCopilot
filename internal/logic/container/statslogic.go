package container

import (
	"context"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/onlyLTY/dockerCopilot/internal/utiles"

	"github.com/zeromicro/go-zero/core/logx"
)

type StatsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewStatsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *StatsLogic {
	return &StatsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// Stats 返回所有 running 容器的 CPU / 内存 / 网络 / 磁盘 IO 快照。
// cpuPercent 与网络速率为相邻两次请求的差值，首次请求为 0。
func (l *StatsLogic) Stats() (resp *types.Resp, err error) {
	resp = &types.Resp{}
	stats, err := utiles.GetContainerStats(l.svcCtx)
	if err != nil {
		resp.Code = 500
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, err
	}
	resp.Msg = "success"
	resp.Data = stats
	return resp, nil
}
