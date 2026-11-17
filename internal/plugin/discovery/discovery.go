// Package discovery 扫描插件目录并幂等登记（ADR-0003、DOC-04 §3.1）。
//
// 发现阶段只读文件，绝不执行插件代码；单个目录失败不影响其他目录。
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/registry"
)

// Report 描述一次扫描的结果，供 API 与日志使用。
type Report struct {
	Inserted  int      `json:"inserted"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	Rejected  int      `json:"rejected"`
	Warnings  []string `json:"warnings,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Scanner 扫描固定目录；扫描串行化，并发触发时合并为一次调用。
type Scanner struct {
	dir      string
	registry *registry.Registry
	logger   *slog.Logger
	mu       sync.Mutex
}

// New 创建扫描器。
func New(dir string, reg *registry.Registry, logger *slog.Logger) *Scanner {
	return &Scanner{dir: dir, registry: reg, logger: logger}
}

// Dir 返回被扫描目录。
func (s *Scanner) Dir() string { return s.dir }

// Scan 执行一次完整扫描。
func (s *Scanner) Scan(ctx context.Context) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var rep Report
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("插件目录 %q 不存在，本次未发现任何插件", s.dir))
			return rep, nil
		}
		return rep, fmt.Errorf("读取插件目录 %q 失败: %w", s.dir, err)
	}

	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		root := filepath.Join(s.dir, entry.Name())
		m, errs, err := manifest.Load(root)
		if err != nil {
			rep.Rejected++
			msg := fmt.Sprintf("插件目录 %q 读取失败：%v", entry.Name(), err)
			rep.Warnings = append(rep.Warnings, msg)
			s.logger.Warn("插件读取失败", "dir", entry.Name(), "error", err)
			continue
		}
		if len(errs) > 0 {
			rep.Rejected++
			msg := fmt.Sprintf("插件目录 %q 校验失败：%s", entry.Name(), errs.Error())
			rep.Warnings = append(rep.Warnings, msg)
			s.logger.Warn("插件校验失败", "dir", entry.Name(), "errors", errs.Error())
			continue
		}

		// 同标识多版本共存时必须显式报告冲突，注册表只保留先登记者（DOC-04 §3.1）。
		if otherDir, ok := seen[m.Identity.ID]; ok {
			rep.Conflicts = append(rep.Conflicts,
				fmt.Sprintf("插件标识 %s 同时出现在目录 %q 与 %q，已保留先发现者", m.Identity.ID, otherDir, entry.Name()))
			continue
		}
		seen[m.Identity.ID] = entry.Name()

		action, upsertErr := s.registry.Upsert(ctx, registry.Plugin{
			ID:             m.Identity.ID,
			Version:        m.Version.Plugin,
			Form:           m.Form,
			InstallPath:    root,
			ManifestYAML:   string(m.Raw),
			ManifestSHA256: m.SHA256,
			ContractOK:     true,
			ContractReason: "",
		})
		if upsertErr != nil {
			return rep, upsertErr
		}
		switch action {
		case registry.ActionInserted:
			rep.Inserted++
			s.logger.Info("插件已登记（默认禁用）", "plugin_id", m.Identity.ID, "version", m.Version.Plugin)
		case registry.ActionUpdated:
			rep.Updated++
			s.logger.Info("插件清单已更新", "plugin_id", m.Identity.ID, "version", m.Version.Plugin)
		default:
			rep.Unchanged++
		}
	}
	return rep, nil
}
