package composefile

import (
	"net/http"

	"github.com/onlyLTY/dockerCopilot/internal/logic/composefile"
	"github.com/onlyLTY/dockerCopilot/internal/svc"
	"github.com/onlyLTY/dockerCopilot/internal/types"
	"github.com/zeromicro/go-zero/rest/httpx"
)

func ListHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.List()
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func ReadHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeReadReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Read(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func SaveHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeFileReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Save(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func DeleteHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeDeleteReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Delete(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func ValidateHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeFileReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Validate(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func DeployHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeDeployReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Deploy(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func ResultHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeJobReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Result(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func HistoryHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.History()
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func VersionsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeReadReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Versions(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func RollbackHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeRollbackReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Rollback(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func TrashHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Trash()
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func RestoreHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ComposeRestoreReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		l := composefile.NewLogic(r.Context(), svcCtx)
		resp, _ := l.Restore(&req)
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}
