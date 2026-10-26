// Package logoshare 在运行时拉取跨端共享的 logo 配置包，
// 与编译期内置包合并后下发。拉取失败保留上一次成功结果并回退内置包，
// 离线/网络受限的 NAS 环境不受影响。
package logoshare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 图标文件在包内 URL 的固定前缀（与前端构建产物 share-icon-<file> 对应）。
const IconPrefix = "share-icon-"

// Pack 共享 logo 包：mappings 为 匹配键 -> 图标文件名（纯文件名，不含路径）。
type Pack struct {
	Version  string            `json:"version"`
	Mappings map[string]string `json:"mappings"`
	Mono     []string          `json:"mono"`
}

// Service 持有远程包的最近成功快照。零值不可用，请用 New。
type Service struct {
	url     string
	client  *http.Client
	remote  *Pack
	lastErr string
	mu      sync.RWMutex
}

// New 创建服务；url 指向 share-logos.json（可为空串表示禁用远程同步）。
func New(rawURL string) *Service {
	return &Service{
		url:    strings.TrimSpace(rawURL),
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *Service) Enabled() bool { return s != nil && s.url != "" }

// IconBase 返回远程图标文件的基 URL（share-logos.json 所在目录，含尾斜杠）。
// 未启用或尚未成功拉取时返回空串，调用方回退到内置路径。
func (s *Service) IconBase() string {
	if !s.Enabled() {
		return ""
	}
	s.mu.RLock()
	hasRemote := s.remote != nil
	s.mu.RUnlock()
	if !hasRemote {
		return ""
	}
	if i := strings.LastIndex(s.url, "/"); i > 0 {
		return s.url[:i+1]
	}
	return ""
}

func (s *Service) RemoteVersion() string {
	if !s.Enabled() {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.remote == nil {
		return ""
	}
	return s.remote.Version
}

func (s *Service) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// Merged 返回内置包与远程包的合并视图。
//   - 同名键远程覆盖内置，且图标值改写为「远程基 URL + share-icon-<file>」
//     （远程包内容更新后无需改路径即可生效）；仅内置的键保留内置相对路径。
//   - mono 取并集。version 优先返回远程包版本，无远程包时为 "embedded"。
func (s *Service) Merged(embedMappings map[string]string, embedMono []string, iconBase string) (map[string]string, []string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	merged := make(map[string]string, len(embedMappings))
	for k, v := range embedMappings {
		merged[k] = v
	}
	monoSet := make(map[string]struct{}, len(embedMono))
	for _, m := range embedMono {
		monoSet[m] = struct{}{}
	}

	version := "embedded"
	if s.remote != nil {
		if s.remote.Version != "" {
			version = s.remote.Version
		}
		for k, file := range s.remote.Mappings {
			if !validIconFile(file) {
				continue
			}
			if iconBase != "" {
				merged[k] = iconBase + IconPrefix + file
			} else {
				merged[k] = file
			}
		}
		for _, m := range s.remote.Mono {
			monoSet[m] = struct{}{}
		}
	}

	mono := make([]string, 0, len(monoSet))
	for m := range monoSet {
		mono = append(mono, m)
	}
	sort.Strings(mono)
	return merged, mono, version
}

// Refresh 拉取并校验远程包；失败保留上一次成功结果，不产生半更新。
func (s *Service) Refresh(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		s.setErr(err)
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.setErr(err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
		s.setErr(err)
		return err
	}
	var pack Pack
	if err := json.NewDecoder(resp.Body).Decode(&pack); err != nil {
		s.setErr(err)
		return err
	}
	if pack.Version == "" || len(pack.Mappings) == 0 {
		err = fmt.Errorf("pack 缺少 version 或 mappings")
		s.setErr(err)
		return err
	}
	for k, file := range pack.Mappings {
		if k == "" || !validIconFile(file) {
			err = fmt.Errorf("pack 含非法条目: %s -> %q", k, file)
			s.setErr(err)
			return err
		}
	}

	s.mu.Lock()
	s.remote = &pack
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

func (s *Service) setErr(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// validIconFile 限制图标文件名为纯文件名（构建产物为平铺布局），
// 防止远程包把路径/协议注入到下发的图标 URL 中。
func validIconFile(file string) bool {
	return file != "" && !strings.Contains(file, "/") && !strings.Contains(file, "\\") &&
		!strings.Contains(file, "..") && strings.Contains(file, ".")
}

var _ = fmt.Sprintf
