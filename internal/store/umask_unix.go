//go:build unix

package store

import "syscall"

// restrictProcessUmask 将进程 umask 收紧到 0077。
//
// 这样做是因为 SQLite 创建 -wal/-shm 附属文件时沿用进程 umask，
// 只在库文件上设置 0600 无法覆盖这两个运行时文件（DOC-08 ST-P-02）。
func restrictProcessUmask() {
	_ = syscall.Umask(0o077)
}
