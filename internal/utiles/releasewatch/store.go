package releasewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
)

// Release 关注的仓库发布信息
type Release struct {
	Repo        string    `json:"repo"`
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// RepoState 单个仓库的已发现记录
type RepoState struct {
	// LastTag 是最近一次已记录的 tag，用于判断是否出现新版本
	LastTag     string    `json:"lastTag"`
	LastCheckAt time.Time `json:"lastCheckAt"`
	LastError   string    `json:"lastError,omitempty"`
}

// Store 关注列表与去重状态的持久化存储
type Store struct {
	mu     sync.RWMutex
	path   string
	data   storeData
	loaded bool
}

type storeData struct {
	Repos  []string             `json:"repos"`
	States map[string]RepoState `json:"states"`
}

func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "release-watch.json"),
		data: storeData{
			Repos:  []string{},
			States: map[string]RepoState{},
		},
	}
}

func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.loaded = true

	content, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var data storeData
	if err := json.Unmarshal(content, &data); err != nil {
		return fmt.Errorf("解析关注列表失败: %w", err)
	}
	if data.Repos == nil {
		data.Repos = []string{}
	}
	if data.States == nil {
		data.States = map[string]RepoState{}
	}
	s.data = data
	return nil
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	// 先写临时文件再 rename，避免进程中断留下半截文件。
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Repos 返回关注列表副本
func (s *Store) Repos() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.load(); err != nil {
		logx.Errorf("读取关注列表失败: %v", err)
	}
	out := make([]string, len(s.data.Repos))
	copy(out, s.data.Repos)
	sort.Strings(out)
	return out
}

// State 返回单个仓库的检查状态
func (s *Store) State(repo string) RepoState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.load(); err != nil {
		logx.Errorf("读取关注状态失败: %v", err)
	}
	return s.data.States[repo]
}

// Add 添加关注仓库
func (s *Store) Add(repo string) error {
	normalized, err := NormalizeRepo(repo)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	for _, existing := range s.data.Repos {
		if strings.EqualFold(existing, normalized) {
			return fmt.Errorf("仓库 %s 已在关注列表中", normalized)
		}
	}
	s.data.Repos = append(s.data.Repos, normalized)
	return s.saveLocked()
}

// Remove 移除关注仓库
func (s *Store) Remove(repo string) error {
	normalized, err := NormalizeRepo(repo)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	kept := make([]string, 0, len(s.data.Repos))
	found := false
	for _, existing := range s.data.Repos {
		if strings.EqualFold(existing, normalized) {
			found = true
			continue
		}
		kept = append(kept, existing)
	}
	if !found {
		return fmt.Errorf("仓库 %s 不在关注列表中", normalized)
	}
	s.data.Repos = kept
	delete(s.data.States, normalized)
	return s.saveLocked()
}

// updateState 记录检查结果
func (s *Store) updateState(repo string, state RepoState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	s.data.States[repo] = state
	return s.saveLocked()
}

// NormalizeRepo 校验并规范化 owner/repo 形式。
// 只接受安全的字符集，避免把任意输入拼进 URL 或文件路径。
func NormalizeRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.Trim(repo, "/")

	if repo == "" {
		return "", errors.New("仓库不能为空")
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("仓库格式应为 owner/repo: %s", repo)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("非法仓库名: %s", repo)
		}
		for _, r := range part {
			valid := r == '-' || r == '_' || r == '.' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !valid {
				return "", fmt.Errorf("非法仓库名: %s", repo)
			}
		}
	}
	return parts[0] + "/" + parts[1], nil
}

// Checker 拉取 GitHub Release 并判断是否有新版本
type Checker struct {
	client  *http.Client
	store   *Store
	token   string
	baseURL string
}

func NewChecker(store *Store, token string) *Checker {
	return &Checker{
		client:  &http.Client{Timeout: 15 * time.Second},
		store:   store,
		token:   token,
		baseURL: "https://api.github.com",
	}
}

// latestReleaseURL 构造请求地址。
// 生产环境（baseURL 为默认值）只允许 https 的 api.github.com，
// 避免把外部输入拼成任意主机地址。
func (c *Checker) latestReleaseURL(repo string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", c.baseURL, repo)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if c.baseURL == "https://api.github.com" {
		if parsed.Scheme != "https" || parsed.Host != "api.github.com" {
			return "", errors.New("非法的请求地址")
		}
	}
	return parsed.String(), nil
}

// LatestRelease 获取仓库最新正式发布版本。
func (c *Checker) LatestRelease(repo string) (*Release, error) {
	endpoint, err := c.latestReleaseURL(repo)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dockerCopilot-release-watch")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("仓库不存在或没有正式发布版本")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, errors.New("GitHub API 频率受限，请配置 GITHUB_TOKEN 或稍后重试")
	default:
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	var payload struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Prerelease  bool      `json:"prerelease"`
		Draft       bool      `json:"draft"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}

	return &Release{
		Repo:        repo,
		Tag:         payload.TagName,
		Name:        payload.Name,
		URL:         payload.HTMLURL,
		PublishedAt: payload.PublishedAt,
		Prerelease:  payload.Prerelease,
		Draft:       payload.Draft,
	}, nil
}

// CheckResult 一次检查的结果
type CheckResult struct {
	Repo    string   `json:"repo"`
	Release *Release `json:"release,omitempty"`
	// IsNew 为 true 表示该版本是上次记录之后新出现的
	IsNew bool   `json:"isNew"`
	Error string `json:"error,omitempty"`
}

// CheckRepo 检查单个仓库。首次检查只记录基线，不把历史版本标记为新版本。
func (c *Checker) CheckRepo(repo string) CheckResult {
	result := CheckResult{Repo: repo}

	release, err := c.LatestRelease(repo)
	if err != nil {
		result.Error = err.Error()
		state := c.store.State(repo)
		state.LastCheckAt = time.Now()
		state.LastError = err.Error()
		_ = c.store.updateState(repo, state)
		return result
	}

	result.Release = release

	state := c.store.State(repo)
	// LastTag 为空说明是首次检查：记录基线，避免把已经存在的版本当作新发布推出去。
	isNew := state.LastTag != "" && state.LastTag != release.Tag && !release.Draft && !release.Prerelease
	result.IsNew = isNew

	state.LastTag = release.Tag
	state.LastCheckAt = time.Now()
	state.LastError = ""
	_ = c.store.updateState(repo, state)

	return result
}

// CheckAll 检查所有关注仓库
func (c *Checker) CheckAll() []CheckResult {
	repos := c.store.Repos()
	results := make([]CheckResult, 0, len(repos))
	for _, repo := range repos {
		results = append(results, c.CheckRepo(repo))
	}
	return results
}
