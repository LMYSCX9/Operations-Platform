package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// ContractVersion 是底座当前实现的契约版本（plugin-spec/07 §2）。
// 提升 MINOR 表示向后兼容增量；提升 MAJOR 表示破坏性变更。
const ContractVersion = "1.2"

// SupportedContracts 是底座可承接的契约版本集合（同一 MAJOR 下的多个 MINOR）。
// 1.1 增量：错误类别、InvocationContext.credential、清单 risk_level；
// 1.2 增量：远端 TCP+mTLS、runtime.listen.port、config[]（ADR-0005/0019/0022）。
var SupportedContracts = []string{"1.2", "1.1", "1.0"}

// Version 表示 MAJOR.MINOR，不使用 PATCH。
type Version struct {
	Major int
	Minor int
}

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

func (v Version) compare(o Version) int {
	if v.Major != o.Major {
		if v.Major < o.Major {
			return -1
		}
		return 1
	}
	if v.Minor != o.Minor {
		if v.Minor < o.Minor {
			return -1
		}
		return 1
	}
	return 0
}

// ParseVersion 解析 "MAJOR.MINOR"。
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return Version{}, fmt.Errorf("版本 %q 必须为 MAJOR.MINOR 形式", s)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return Version{}, fmt.Errorf("版本 %q 的主版本号非法", s)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return Version{}, fmt.Errorf("版本 %q 的次版本号非法", s)
	}
	return Version{Major: major, Minor: minor}, nil
}

// Constraint 是单个比较式，操作符仅支持 >=、<、=。
type Constraint struct {
	Op string
	V  Version
}

// Range 是空格分隔的约束集合，全部满足才算命中。
type Range struct {
	Raw         string
	Constraints []Constraint
}

// ParseRange 解析插件声明的契约范围，例如 ">=1.0 <2.0"。
func ParseRange(s string) (Range, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Range{}, fmt.Errorf("契约范围不得为空")
	}
	fields := strings.Fields(raw)
	r := Range{Raw: raw}
	eqSeen := false
	for _, f := range fields {
		var op string
		switch {
		case strings.HasPrefix(f, ">="):
			op = ">="
		case strings.HasPrefix(f, "<"):
			op = "<"
		case strings.HasPrefix(f, "="):
			op = "="
		default:
			return Range{}, fmt.Errorf("约束 %q 缺少受支持的操作符（仅支持 >= < =）", f)
		}
		v, err := ParseVersion(strings.TrimSpace(strings.TrimPrefix(f, op)))
		if err != nil {
			return Range{}, err
		}
		if op == "=" {
			if eqSeen {
				return Range{}, fmt.Errorf("契约范围中最多允许一个 = 约束")
			}
			eqSeen = true
		}
		r.Constraints = append(r.Constraints, Constraint{Op: op, V: v})
	}
	return r, nil
}

// Contains 判断版本是否满足全部约束。
func (r Range) Contains(v Version) bool {
	for _, c := range r.Constraints {
		cmp := v.compare(c.V)
		switch c.Op {
		case ">=":
			if cmp < 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "=":
			if cmp != 0 {
				return false
			}
		}
	}
	return true
}

// CheckContract 判定插件声明的范围是否与底座支持的契约兼容。
func CheckContract(r Range) error {
	_, err := ResolveContract(r)
	return err
}

// ResolveContract 返回底座可承接的最高契约版本；无交集时报错并列出支持范围。
// 版本判定发生在发现/启用阶段，不在调用阶段临时降级（ADR-0012）。
func ResolveContract(r Range) (string, error) {
	for _, s := range SupportedContracts {
		v, err := ParseVersion(s)
		if err != nil {
			continue
		}
		if r.Contains(v) {
			return s, nil
		}
	}
	return "", fmt.Errorf("version.contract: 声明范围 %q 与底座支持的契约版本 %s 无交集",
		r.Raw, strings.Join(SupportedContracts, ", "))
}
