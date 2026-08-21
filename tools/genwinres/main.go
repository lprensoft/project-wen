// genwinres 从 internal/version 生成 Windows 可执行文件的版本资源与程序图标（.syso）。
//
// 版本号的唯一来源是 internal/version.Version，本工具把它写进 VERSIONINFO 资源，
// 使 wen.exe 的「属性 → 详细信息」里显示同一个版本——发布产物的版本与界面、
// /status、启动日志由此保持一致。资源里同时编入程序图标，取 tools/genicon 生成的
// 那份 .ico——与浏览器标签页图标同源。生成的 .syso 放在 cmd/wen/ 下随包入库，
// go build 会自动把同目录的 .syso 链入，因此日常构建命令不变；
// 只有改动版本号或换了图标后需要重新执行 go generate ./cmd/wen。
package main

import (
	"fmt"
	"log"
	"regexp"
	"strconv"

	"github.com/josephspurrier/goversioninfo"

	"wen/internal/version"
)

// semverRe 匹配 vMAJOR.MINOR.PATCH。资源里的第四段（build）恒为 0。
var semverRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// copyright 是 exe 属性里的「版权」一行，与 LICENSE 的署名保持一致。
// 年份写死而不取当前年：.syso 随库提交，发布流水线会检查 go generate 之后无 diff，
// 取 time.Now() 会让这份生成物在跨年那天自己变成 diff，把发布卡住。
const copyright = "Copyright (c) 2026 Hao Ren. MIT License."

func main() {
	m := semverRe.FindStringSubmatch(version.Version)
	if m == nil {
		log.Fatalf("版本号 %q 不是 vX.Y.Z 形式，无法生成版本资源", version.Version)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])

	// 资源里的字符串版本按 Windows 惯例不带 v 前缀（x.y.z），带上会触发解析告警
	plain := fmt.Sprintf("%d.%d.%d", major, minor, patch)
	fv := goversioninfo.FileVersion{Major: major, Minor: minor, Patch: patch, Build: 0}
	vi := &goversioninfo.VersionInfo{
		FixedFileInfo: goversioninfo.FixedFileInfo{
			FileVersion:    fv,
			ProductVersion: fv,
			FileFlagsMask:  "3f",
			FileFlags:      "00",
			FileOS:         "040004",
			FileType:       "01",
		},
		StringFileInfo: goversioninfo.StringFileInfo{
			ProductName:      "Wen Agent",
			FileDescription:  "Wen Agent",
			InternalName:     "wen",
			OriginalFilename: "wen.exe",
			FileVersion:      plain,
			ProductVersion:   plain,
			LegalCopyright:   copyright,
		},
		VarFileInfo: goversioninfo.VarFileInfo{
			Translation: goversioninfo.Translation{LangID: 0x0804, CharsetID: 0x04B0}, // 简体中文 / Unicode
		},
		// 与 Web UI 的浏览器标签页图标同一个文件，由 tools/genicon 从 logo 图源生成
		IconPath: "../../internal/server/webui/favicon.ico",
	}
	vi.Build()
	vi.Walk()

	// 按架构各生成一份：go build 只链入与目标架构匹配的 <name>_windows_<arch>.syso
	for _, arch := range []string{"amd64", "arm64"} {
		out := fmt.Sprintf("resource_windows_%s.syso", arch)
		if err := vi.WriteSyso(out, arch); err != nil {
			log.Fatalf("生成 %s 失败: %v", out, err)
		}
		fmt.Printf("已生成 %s（%s）\n", out, version.Version)
	}
}
