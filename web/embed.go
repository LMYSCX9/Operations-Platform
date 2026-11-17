// Package web 提供内嵌的前端静态资源。
//
// 内嵌方式遵循 ADR-0001 的推荐方案（Go embed），不引入 Node 构建链。
// prototype/index.html 是页面的事实来源；web/dist/index.html 是构建脚本同步出的产物副本，
// 因为 go:embed 不允许引用包目录之外的文件。
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist/index.html
var files embed.FS

// Dist 返回内嵌产物的根文件系统，盘中路径为 index.html。
func Dist() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		// embed 指令已在编译期保证 dist 目录存在，此分支不可达；
		// 保留 panic 是为了让构建期问题不会被静默吞掉。
		panic("web: 内嵌资源目录 dist 缺失: " + err.Error())
	}
	return sub
}
