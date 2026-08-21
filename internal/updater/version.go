package updater

import (
	"regexp"
	"strconv"
	"strings"
)

// 版本号的比较。
//
// 本项目的版本号只有两种形态：正式版 `v0.6.1`（internal/version 里的那一个，也是
// 发布 tag），以及开发版构建注入的 `git describe` 形态 `v0.6.1-3-g29a95a4`
// （读作「v0.6.1 之后的第 3 个提交」）。预发布 tag 形如 `v0.1.3-rc1`。
//
// 判定「该不该更新」时三者的关系是：v0.6.1-rc1 < v0.6.1 < v0.6.1-3-g29a95a4 < v0.6.2。
// 开发版排在同号正式版之后，于是拿正式版去覆盖一个基于它的开发版不会被判成升级——
// 那是降级，用户在试的东西会被悄悄换掉。

// devSuffix 匹配 git describe 的尾巴：-<提交数>-g<提交号>。
var devSuffix = regexp.MustCompile(`-(\d+)-g[0-9a-f]{4,}$`)

// Version 是解析后的版本号。
type Version struct {
	Raw     string
	Major   int
	Minor   int
	Patch   int
	Pre     string // 预发布后缀（rc1 之类），正式版为空
	Dev     bool   // git describe 形态的开发版构建
	AheadOf int    // 开发版：距基准正式版的提交数
	parsed  bool
}

// ParseVersion 解析版本号。无法识别时第二个返回值为 false——
// 认不出的版本一律不参与比较，宁可不提示更新，也不要拿一个猜出来的大小做决定。
func ParseVersion(s string) (Version, bool) {
	v := Version{Raw: strings.TrimSpace(s)}
	t := strings.TrimPrefix(v.Raw, "v")
	if m := devSuffix.FindStringSubmatch(t); m != nil {
		v.Dev = true
		v.AheadOf, _ = strconv.Atoi(m[1])
		t = t[:len(t)-len(m[0])]
	}
	if i := strings.IndexAny(t, "-+"); i >= 0 {
		v.Pre = t[i+1:]
		t = t[:i]
	}
	parts := strings.Split(t, ".")
	if len(parts) != 3 {
		return Version{Raw: v.Raw}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{Raw: v.Raw}, false
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	v.parsed = true
	return v, true
}

// Less 报告 v 是否排在 o 之前。
func (v Version) Less(o Version) bool {
	switch {
	case v.Major != o.Major:
		return v.Major < o.Major
	case v.Minor != o.Minor:
		return v.Minor < o.Minor
	case v.Patch != o.Patch:
		return v.Patch < o.Patch
	}
	// 同一个三段号内部：预发布 < 正式版 < 开发版
	if r := rank(v); r != rank(o) {
		return r < rank(o)
	}
	if v.Pre != o.Pre {
		return v.Pre < o.Pre
	}
	return v.AheadOf < o.AheadOf
}

// rank 给同号版本的三种形态排序：预发布 0、正式版 1、开发版 2。
func rank(v Version) int {
	switch {
	case v.Dev:
		return 2
	case v.Pre != "":
		return 0
	default:
		return 1
	}
}

// Base 返回去掉开发版尾巴后的基准正式版（`v0.6.1-3-g29a95a4` → `v0.6.1`）。
func (v Version) Base() string {
	if !v.parsed {
		return v.Raw
	}
	s := "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Newer 报告 tag 是否比 current 新。两者中任何一个认不出来都返回 false。
func Newer(current, tag string) bool {
	cur, ok1 := ParseVersion(current)
	next, ok2 := ParseVersion(tag)
	if !ok1 || !ok2 {
		return false
	}
	return cur.Less(next)
}
