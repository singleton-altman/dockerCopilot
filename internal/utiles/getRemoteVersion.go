package utiles

import (
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/onlyLTY/dockerCopilot/internal/config"
	"github.com/zeromicro/go-zero/core/logx"
)

// 本 fork 独立发布：版本源与升级包均来自 singleton-altman/dockerCopilot。
const (
	versionRepoURL         = "https://raw.githubusercontent.com/singleton-altman/dockerCopilot/UGREEN/version"
	releaseBaseDownloadURL = "https://github.com/singleton-altman/dockerCopilot/releases/download"
	channelSuffixUGRN      = "-UGREEN"
	channelSuffixFNS       = "-FNOS"
)

// NormalizeChannelVersion 去掉渠道后缀（-UGREEN/-FNOS），
// 只保留语义版本用于本地/远程比较：v2.2.1-UGREEN 与 v2.2.1 视为同一版本。
func NormalizeChannelVersion(v string) string {
	v = strings.TrimSuffix(v, channelSuffixUGRN)
	v = strings.TrimSuffix(v, channelSuffixFNS)
	return v
}

func GetRemoteVersion() (remoteVersion string, err error) {
	githubProxy := os.Getenv("githubProxy")
	if githubProxy != "" {
		githubProxy = strings.TrimRight(githubProxy, "/") + "/"
	}
	versionURL := githubProxy + versionRepoURL
	remoteVersion, err = fetchVersionFromURL(versionURL)
	if err != nil {
		return "0.0.0", err
	}

	localVersion := config.Version
	if strings.Contains(localVersion, channelSuffixFNS) {
		logx.Infof("飞牛版本，无需在线更新")
		return NormalizeChannelVersion(localVersion), nil
	}
	normalized := NormalizeChannelVersion(localVersion)
	if normalized == remoteVersion {
		logx.Info("版本一致:", normalized)
		return remoteVersion, nil
	} else {
		logx.Infof("版本不一致! 本地: %s, 远程: %s\n", normalized, remoteVersion)
		return remoteVersion, nil
	}

}

func fetchVersionFromURL(url string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logx.Error("关闭Body失败:", err)
		}
	}(resp.Body)

	versionData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(versionData)), nil
}
