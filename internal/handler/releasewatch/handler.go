package releasewatch

import (
	"net/http"

	"github.com/onlyLTY/dockerCopilot/internal/logic/releasewatch"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func ListHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := releasewatch.NewLogic(r.Context(), svcCtx)
		resp, _ := l.List()
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func AddHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ReleaseRepoReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := releasewatch.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Add(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func RemoveHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ReleaseRepoReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := releasewatch.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Remove(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func CheckHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := releasewatch.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Check()
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}
