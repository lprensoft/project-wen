// Package runlock 登记「本配置目录下有一个服务正在运行，它监听在哪里」。
//
// 存在的理由是配置工具要知道该走在线模式还是离线模式：服务在跑时必须经它的
// HTTP 接口改配置（改动立即生效，也不会被服务端的全量状态重写抹掉），服务没跑时
// 才直接改状态文件。靠猜端口是不可靠的——服务可能用 -c 指了另一份配置、或用 -p
// 改了端口，而配置工具读到的是配置文件里的值。
//
// 文件只是个提示，不是锁：真正的存活判定由调用方去连那个地址。陈旧的文件（进程被
// kill -9 掉）因此不会造成任何误判，只是让调用方多探一次。
package runlock

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const fileName = "wen.lock"

// Info 是运行中实例登记的信息。
type Info struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"` // 实际监听地址 host:port（可能与配置文件不同）
	Version string `json:"version"`
	Started string `json:"started"`
}

// Path 返回登记文件的路径。
func Path(dir string) string {
	return filepath.Join(dir, fileName)
}

// Acquire 写入登记文件，返回用于删除它的函数。
// 写入失败不是致命错误——配置工具会退回离线模式，服务本身照常运行。
func Acquire(dir string, info Info) (release func(), err error) {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return func() {}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, err
	}
	path := Path(dir)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return func() {}, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// Read 读取登记文件。第二个返回值为 false 表示没有登记（文件缺失或内容损坏）。
// 文件存在不代表进程还活着，调用方需要自行探活。
func Read(dir string) (Info, bool) {
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		return Info{}, false
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil || info.Addr == "" {
		return Info{}, false
	}
	return info, true
}
