//go:build !unix

package store

// restrictProcessUmask 在非 Unix 平台上为空实现。
//
// Windows 没有 umask 语义，权限需通过 ACL 限定；ACL 细化尚未实现，
// 属于 M0 已知限制（见 docs/11-implementation-baseline-m0.md）。
func restrictProcessUmask() {}
