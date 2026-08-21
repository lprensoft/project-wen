// Package updater 从 GitHub Releases 取新版本并替换当前可执行文件。
//
// 整条链路只做四件事：查最新的正式版、下载对应本机 GOOS/GOARCH 的那个包、按发布里
// 的 SHA256SUMS 校验、把二进制换成新的。
//
// 几条刻意的取舍：
//
//   - **仓库写死在代码里**。它决定了「从哪里下载一个即将被执行的文件」，做成配置项
//     等于允许把程序指向任意来源。
//   - **只认正式版**。滚动的 dev 预发布没有可比较的版本号（tag 固定叫 dev），拿它
//     判断新旧只能靠时间戳，而「比我新」与「值得换上去」在开发版上不是一回事。
//   - **校验和只保证下载没坏**，不构成对发布本身被篡改的防护——信任锚是 TLS 与
//     GitHub 账号。要更强得给产物加签名，那是另一件事。
//   - **换上去之前先跑一次新二进制**（见 SmokeTest）。校验和过了不代表这个程序能在
//     这台机器上起来（缺依赖、被杀软改写、下错架构），而这一步的代价几乎为零。
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo 是发布仓库。见包注释：它不做成配置项。
const DefaultRepo = "lprensoft/project-wen"

// defaultAPI 是 GitHub API 的根地址，测试里替换成本地服务。
const defaultAPI = "https://api.github.com"

// sumsAsset 是发布里那份校验和清单的文件名，由发布流水线生成。
const sumsAsset = "SHA256SUMS.txt"

const (
	// apiTimeout 是一次接口调用的时限。查版本是个小请求，卡住不该让界面一直转。
	apiTimeout = 20 * time.Second
	// maxAPIBody 挡住异常大的响应，发布信息只有几十 KB。
	maxAPIBody = 4 << 20
	// maxAssetSize 是可接受的包体上限，当前的包在 10 MB 上下。
	maxAssetSize = 256 << 20
)

// Asset 是发布里的一个附件。
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Release 是一次发布。
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
	HTMLURL     string    `json:"html_url"`
}

// Client 访问 GitHub 的发布接口。零值不可用，用 NewClient 构造。
type Client struct {
	HTTP *http.Client
	API  string // 接口根地址，为空用 api.github.com
	Repo string // owner/name，为空用 DefaultRepo
	// UA 是 User-Agent。GitHub 要求带一个，不带会被拒。
	UA string
}

// NewClient 按默认参数构造。version 用于 User-Agent，出问题时对方日志里认得出是谁。
func NewClient(version string) *Client {
	return &Client{
		HTTP: &http.Client{Timeout: apiTimeout},
		API:  defaultAPI,
		Repo: DefaultRepo,
		UA:   "wen/" + strings.TrimPrefix(version, "v"),
	}
}

func (c *Client) api() string {
	if c.API != "" {
		return strings.TrimRight(c.API, "/")
	}
	return defaultAPI
}

func (c *Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: apiTimeout}
}

// Latest 取最新的正式版。
//
// 用 releases/latest 而不是列表：这个接口本来就跳过草稿与预发布，滚动的 dev 预发布
// 因此天然被挡在外面，不必在这边再过滤一遍。
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.api(), c.repo())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("连接 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody))
	if err != nil {
		return Release{}, fmt.Errorf("读取响应失败: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("仓库 %s 还没有正式发布", c.repo())
	case http.StatusForbidden, http.StatusTooManyRequests:
		// 未鉴权的调用按 IP 限流（每小时 60 次）。说清楚是限流而不是坏了，
		// 否则用户只会看到一个没头没尾的 403。
		return Release{}, fmt.Errorf("GitHub 接口暂时限流，请过一会儿再试（%s）", resetHint(resp))
	default:
		return Release{}, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}

	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return Release{}, fmt.Errorf("解析发布信息失败: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, fmt.Errorf("发布信息里没有版本号")
	}
	return rel, nil
}

// resetHint 把限流重置时刻说成人话；拿不到就退回一句笼统的说明。
func resetHint(resp *http.Response) string {
	v := resp.Header.Get("X-RateLimit-Reset")
	if v == "" {
		return "已达接口调用上限"
	}
	var sec int64
	if _, err := fmt.Sscanf(v, "%d", &sec); err != nil || sec <= 0 {
		return "已达接口调用上限"
	}
	d := time.Until(time.Unix(sec, 0))
	if d <= 0 {
		return "已达接口调用上限"
	}
	return fmt.Sprintf("约 %d 分钟后恢复", int(d.Minutes())+1)
}

// AssetName 拼出某个版本在某个平台上的包名，与发布流水线的命名一致。
func AssetName(tag, goos, goarch string) string {
	name := fmt.Sprintf("wen-%s-%s-%s", tag, goos, goarch)
	if goos == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

// BinaryName 是包里那个可执行文件的名字。
func BinaryName(goos string) string {
	if goos == "windows" {
		return "wen.exe"
	}
	return "wen"
}

// Plan 是一次更新的执行计划：下哪个包、期望的校验和是多少。
type Plan struct {
	Release Release
	Asset   Asset
	Sum     string // 期望的 sha256（小写十六进制）
}

// Prepare 从发布里挑出本机平台的包，并取回它的校验和。
//
// 校验和缺失时直接失败而不是跳过校验：这份清单是发布流水线必出的产物，它不在意味着
// 这次发布不完整，而「校验不了就不校验」正是最不该有的降级。
func (c *Client) Prepare(ctx context.Context, rel Release) (Plan, error) {
	want := AssetName(rel.Tag, runtime.GOOS, runtime.GOARCH)
	var (
		pkg  *Asset
		sums *Asset
	)
	for i := range rel.Assets {
		switch rel.Assets[i].Name {
		case want:
			pkg = &rel.Assets[i]
		case sumsAsset:
			sums = &rel.Assets[i]
		}
	}
	if pkg == nil {
		return Plan{}, fmt.Errorf("%s 没有提供 %s/%s 的包（%s）", rel.Tag, runtime.GOOS, runtime.GOARCH, want)
	}
	if pkg.Size > maxAssetSize {
		return Plan{}, fmt.Errorf("包体积异常（%d 字节），已中止", pkg.Size)
	}
	if sums == nil {
		return Plan{}, fmt.Errorf("%s 没有提供 %s，无法校验下载内容", rel.Tag, sumsAsset)
	}

	raw, err := c.get(ctx, sums.URL, maxAPIBody)
	if err != nil {
		return Plan{}, fmt.Errorf("下载校验和清单失败: %w", err)
	}
	sum, ok := lookupSum(string(raw), want)
	if !ok {
		return Plan{}, fmt.Errorf("%s 里没有 %s 的校验和", sumsAsset, want)
	}
	return Plan{Release: rel, Asset: *pkg, Sum: sum}, nil
}

// lookupSum 在 sha256sum 格式的清单里找某个文件名对应的哈希。
func lookupSum(text, name string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		// 第二列可能带 sha256sum 的二进制标记前缀 *
		if strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// get 取一个小文件（校验和清单）。
func (c *Client) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("返回 %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
