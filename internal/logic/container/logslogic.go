package container

import (
	"context"
	"strconv"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/onlyLTY/dockerCopilot/internal/utiles"

	"github.com/zeromicro/go-zero/core/logx"
)

type LogsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewLogsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *LogsLogic {
	return &LogsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *LogsLogic) Logs(req *types.ContainerLogsReq) (resp *types.Resp, err error) {
	resp = &types.Resp{}
	output, err := utiles.ContainerLogs(
		l.svcCtx,
		req.Id,
		utiles.ParseLogTail(strconv.Itoa(req.Tail)),
		req.Timestamps,
	)
	if err != nil {
		resp.Code = 400
		resp.Msg = err.Error()
		resp.Data = map[string]interface{}{}
		return resp, err
	}

	resp.Code = 200
	resp.Msg = "success"
	resp.Data = map[string]interface{}{
		"id":         req.Id,
		"logs":       utiles.NormalizeLogOutput(output),
		"lineCount":  countLines(output),
		"timestamps": req.Timestamps,
	}
	return resp, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			count++
		}
	}
	if s[len(s)-1] != '\n' {
		count++
	}
	return count
}
