package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// 目录名称来自 DOC-08 §4，属于规范布局，不得随意调整。
const (
	DirDB         = "db"
	DirBackups    = "backups"
	DirPlugins    = "plugins"
	DirPluginData = "plugin-data"
	DirTmp        = "tmp"
)

var managedDirs = []string{DirDB, DirBackups, DirPlugins, DirPluginData, DirTmp}

// FilesystemChecker 用于在启动时拒绝网络文件系统（DOC-08 §5）。
//
// 具体检测方式尚未确定（DOC-08 §11），M0 只留出接口；
// 默认实现不做检测，待检测方案确认后替换。
type FilesystemChecker interface {
	CheckLocal(ctx context.Context, path string) error
}

// NoopFilesystemChecker 是 M0 的占位实现：不做任何检测。
type NoopFilesystemChecker struct{}

// CheckLocal 始终返回 nil，表示「未做检测」，而不是「已确认是本地文件系统」。
func (NoopFilesystemChecker) CheckLocal(context.Context, string) error {
	return nil
}

// InitDataDir 校验并初始化数据目录布局。
//
// 目录与文件权限按 DOC-08 §4.1 收紧到 0700；Windows 上 os.Chmod 只能控制只读位，
// ACL 细化尚未实现，属于已知限制（见 docs/11-implementation-baseline-m0.md）。
func InitDataDir(ctx context.Context, logger *slog.Logger, root string, checker FilesystemChecker) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("数据目录必须为绝对路径，当前为 %q（DOC-08 ST-D-01）", root)
	}
	if checker == nil {
		checker = NoopFilesystemChecker{}
	}
	if err := checker.CheckLocal(ctx, root); err != nil {
		return fmt.Errorf("数据目录文件系统校验失败: %w", err)
	}

	// 配置文件未指定数据目录时已在配置层拒绝启动；此处只负责在路径合法时创建并收紧权限。
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("创建数据目录 %q 失败: %w", root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("读取数据目录信息失败: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("数据目录 %q 不是目录", root)
	}
	restrictPermissions(logger, root)

	for _, name := range managedDirs {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建数据子目录 %q 失败: %w", dir, err)
		}
		restrictPermissions(logger, dir)
	}

	// MkdirAll 成功不代表目录可写（例如只读挂载），必须做一次真实写入探测。
	probe, err := os.CreateTemp(root, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("数据目录不可写: %w", err)
	}
	probePath := probe.Name()
	defer os.Remove(probePath)

	if _, err := probe.Write([]byte("ok")); err != nil {
		probe.Close()
		return fmt.Errorf("数据目录写入探测失败: %w", err)
	}
	if err := probe.Close(); err != nil {
		return fmt.Errorf("数据目录写入探测关闭失败: %w", err)
	}
	return nil
}

// restrictPermissions 尝试收紧权限。ST-P-04 规定权限过宽时「是否拒绝启动」未定，
// 因此这里只告警，不阻断启动。
func restrictPermissions(logger *slog.Logger, path string) {
	if err := os.Chmod(path, 0o700); err != nil {
		logger.Warn("收紧目录权限失败，请人工检查", "path", path, "error", err)
	}
}

func restrictFilePermissions(logger *slog.Logger, path string) {
	if err := os.Chmod(path, 0o600); err != nil {
		logger.Warn("收紧文件权限失败，请人工检查", "path", path, "error", err)
	}
}
