package manifest

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

//go:embed schema.json
var schemaJSON []byte

var (
	idPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	sectionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	pluginVerPat   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	credentialKey  = regexp.MustCompile(`(?i)^(password|passwd|secret|token|api[_-]?key|apikey|private[_-]?key|credential|credentials|access[_-]?key|secret[_-]?key)$`)

	schemaOnce sync.Once
	schemaVal  *jsonschema.Schema
	schemaErr  error
)

// FieldError 是可定位到字段路径的校验错误。禁止只返回「校验失败」。
type FieldError struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Reason }

// FieldErrors 是校验错误集合。
type FieldErrors []FieldError

func (es FieldErrors) Error() string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

func (es FieldErrors) HasErrors() bool { return len(es) > 0 }

func schemaValidator() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		c := jsonschema.NewCompiler()
		if err := c.AddResource("manifest.json", bytes.NewReader(schemaJSON)); err != nil {
			schemaErr = fmt.Errorf("加载清单 schema 失败: %w", err)
			return
		}
		schemaVal, schemaErr = c.Compile("manifest.json")
		if schemaErr != nil {
			schemaErr = fmt.Errorf("编译清单 schema 失败: %w", schemaErr)
		}
	})
	return schemaVal, schemaErr
}

// Load 读取并校验 <root>/plugin.yaml。
// 返回的 err 只用于文件读取/解码一类环境错误；字段问题放在 FieldErrors 中。
func Load(root string) (*Manifest, FieldErrors, error) {
	path := filepath.Join(root, FileName)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, FieldErrors{{Path: FileName, Reason: "清单文件不存在"}}, nil
		}
		return nil, nil, fmt.Errorf("读取清单 %q 失败: %w", path, err)
	}
	if info.IsDir() {
		return nil, FieldErrors{{Path: FileName, Reason: "清单路径是目录而非文件"}}, nil
	}
	if info.Size() > MaxFileSize {
		return nil, FieldErrors{{Path: FileName, Reason: fmt.Sprintf("清单体积超过 %d 字节上限", MaxFileSize)}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取清单 %q 失败: %w", path, err)
	}
	m, errs := Decode(data, root)
	return m, errs, nil
}

// Decode 解析并校验清单字节；root 用于校验包内相对路径。
func Decode(data []byte, root string) (*Manifest, FieldErrors) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, FieldErrors{{Path: FileName, Reason: "YAML 解析失败: " + err.Error()}}
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, FieldErrors{{Path: FileName, Reason: "清单结构不符合契约: " + err.Error()}}
	}

	sum := sha256.Sum256(data)
	m.Root = root
	m.Raw = data
	m.SHA256 = hex.EncodeToString(sum[:])

	var errs FieldErrors
	errs = append(errs, validateSchema(raw)...)
	errs = append(errs, validateSemantics(&m, raw)...)
	errs = dedupe(errs)
	if len(errs) > 0 {
		return nil, errs
	}
	return &m, nil
}

func dedupe(in FieldErrors) FieldErrors {
	seen := make(map[string]struct{}, len(in))
	out := make(FieldErrors, 0, len(in))
	for _, e := range in {
		key := e.Path + "\x00" + e.Reason
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func validateSchema(raw map[string]any) FieldErrors {
	schema, err := schemaValidator()
	if err != nil {
		return FieldErrors{{Path: "plugin.yaml", Reason: err.Error()}}
	}
	if err := schema.Validate(raw); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return FieldErrors{{Path: "plugin.yaml", Reason: err.Error()}}
		}
		var out FieldErrors
		collectSchemaErrors(ve, &out)
		if len(out) == 0 {
			out = append(out, FieldError{Path: "plugin.yaml", Reason: ve.Message})
		}
		return out
	}
	return nil
}

func collectSchemaErrors(ve *jsonschema.ValidationError, out *FieldErrors) {
	if len(ve.Causes) == 0 {
		path := pointerToPath(ve.InstanceLocation)
		if path == "" {
			path = "plugin.yaml"
		}
		*out = append(*out, FieldError{Path: path, Reason: ve.Message})
		return
	}
	for _, c := range ve.Causes {
		collectSchemaErrors(c, out)
	}
}

// pointerToPath 把 JSON Pointer（/capabilities/0/id）转成可读路径（capabilities[0].id）。
func pointerToPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	var b strings.Builder
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if isDigits(part) {
			b.WriteString("[" + part + "]")
		} else {
			if b.Len() > 0 {
				b.WriteString(".")
			}
			b.WriteString(part)
		}
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validateSemantics(m *Manifest, raw map[string]any) FieldErrors {
	var errs FieldErrors

	if m.Identity.ID != "" && !idPattern.MatchString(m.Identity.ID) {
		errs = append(errs, FieldError{Path: "identity.id", Reason: `必须匹配 ^[a-z][a-z0-9-]*$`})
	}
	if m.Version.Plugin != "" && !pluginVerPat.MatchString(m.Version.Plugin) {
		errs = append(errs, FieldError{Path: "version.plugin", Reason: "必须为 MAJOR.MINOR.PATCH"})
	}

	if m.Version.Contract != "" {
		rng, err := ParseRange(m.Version.Contract)
		if err != nil {
			errs = append(errs, FieldError{Path: "version.contract", Reason: err.Error()})
		} else if err := CheckContract(rng); err != nil {
			errs = append(errs, FieldError{Path: "version.contract", Reason: err.Error()})
		}
	}

	seenCap := make(map[string]struct{}, len(m.Capabilities))
	for i, c := range m.Capabilities {
		base := fmt.Sprintf("capabilities[%d].id", i)
		prefix := m.Identity.ID + "."
		if m.Identity.ID == "" {
			continue
		}
		if !strings.HasPrefix(c.ID, prefix) {
			errs = append(errs, FieldError{Path: base, Reason: fmt.Sprintf("能力标识必须以插件标识加前缀 %q 开头", prefix)})
		} else {
			name := strings.TrimPrefix(c.ID, prefix)
			if !sectionPattern.MatchString(name) {
				errs = append(errs, FieldError{Path: base, Reason: `能力名部分必须匹配 ^[a-z][a-z0-9-]*$`})
			}
		}
		if _, ok := seenCap[c.ID]; ok {
			errs = append(errs, FieldError{Path: base, Reason: "能力标识重复"})
		}
		seenCap[c.ID] = struct{}{}

		for _, ref := range []struct {
			field string
			value string
		}{
			{"input_schema_ref", c.InputSchemaRef},
			{"output_schema_ref", c.OutputSchemaRef},
		} {
			if ref.value == "" {
				continue
			}
			full, err := resolveInside(m.Root, ref.value)
			if err != nil {
				errs = append(errs, FieldError{Path: fmt.Sprintf("capabilities[%d].%s", i, ref.field), Reason: err.Error()})
				continue
			}
			if _, err := os.Stat(full); err != nil {
				errs = append(errs, FieldError{
					Path:   fmt.Sprintf("capabilities[%d].%s", i, ref.field),
					Reason: fmt.Sprintf("文件 %q 不存在或不可读", ref.value),
				})
			}
		}
	}

	entryPath, entryField := entryOf(m)
	if entryPath != "" {
		full, err := resolveInside(m.Root, entryPath)
		if err != nil {
			errs = append(errs, FieldError{Path: entryField, Reason: err.Error()})
		} else {
			// Windows 上清单按契约写不带后缀的可执行文件名，实际文件是 .exe，
			// 与 runtime.resolveExecutable 的回退规则保持一致。
			if runtime.GOOS == "windows" {
				if _, statErr := os.Stat(full); statErr != nil {
					if alt, altErr := os.Stat(full + ".exe"); altErr == nil && !alt.IsDir() {
						full = full + ".exe"
					}
				}
			}
			info, statErr := os.Stat(full)
			switch {
			case statErr != nil:
				errs = append(errs, FieldError{Path: entryField, Reason: fmt.Sprintf("入口文件 %q 不存在", entryPath)})
			case info.IsDir():
				errs = append(errs, FieldError{Path: entryField, Reason: "入口必须是可执行文件而非目录"})
			case runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0:
				errs = append(errs, FieldError{Path: entryField, Reason: "入口文件没有可执行权限"})
			}
		}
	}

	// config[] 是契约 1.2 增量：键唯一，默认值必须与声明类型一致。
	seenCfg := make(map[string]struct{}, len(m.Config))
	for i, f := range m.Config {
		base := fmt.Sprintf("config[%d]", i)
		if _, ok := seenCfg[f.Key]; ok {
			errs = append(errs, FieldError{Path: base + ".key", Reason: "配置键重复"})
		}
		seenCfg[f.Key] = struct{}{}
		if f.Default != nil && !configDefaultMatches(f) {
			errs = append(errs, FieldError{
				Path:   base + ".default",
				Reason: fmt.Sprintf("默认值类型与声明的 %s 不匹配", f.Type),
			})
		}
		if f.Type == "enum" && len(f.Enum) == 0 {
			errs = append(errs, FieldError{Path: base + ".enum", Reason: "type=enum 时必须声明枚举值"})
		}
	}
	if m.Form == "command" && m.Runtime != nil && m.Runtime.Listen != nil {
		errs = append(errs, FieldError{Path: "runtime.listen", Reason: "仅 daemon 形态允许声明监听端口"})
	}

	// 递归检查非法键：v1 禁止 dependencies 与任何凭据字段。
	errs = append(errs, scanForbiddenKeys(raw, "")...)
	return errs
}

// configDefaultMatches 校验默认值与声明类型一致；enum 必须命中枚举值。
func configDefaultMatches(f ConfigField) bool {
	switch f.Type {
	case "string":
		_, ok := f.Default.(string)
		return ok
	case "boolean":
		_, ok := f.Default.(bool)
		return ok
	case "integer":
		switch v := f.Default.(type) {
		case int, int64, uint64, float64:
			if n, ok := v.(float64); ok {
				return n == float64(int64(n))
			}
			return true
		}
		return false
	case "number":
		switch f.Default.(type) {
		case int, int64, uint64, float64:
			return true
		}
		return false
	case "enum":
		s, ok := f.Default.(string)
		if !ok {
			return false
		}
		for _, e := range f.Enum {
			if e == s {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func entryOf(m *Manifest) (path, field string) {
	switch m.Form {
	case "command":
		return m.Entry.Executable, "entry.executable"
	case "daemon":
		return m.Entry.Launch, "entry.launch"
	default:
		return "", ""
	}
}

// resolveInside 解析包内相对路径，并拒绝绝对路径与目录穿越。
func resolveInside(root, rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", fmt.Errorf("路径 %q 不能是绝对路径", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) ||
		strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, `..\`) {
		return "", fmt.Errorf("路径 %q 越出插件包目录", rel)
	}
	return filepath.Join(root, cleaned), nil
}

func scanForbiddenKeys(node any, prefix string) FieldErrors {
	var errs FieldErrors
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if strings.EqualFold(k, "dependencies") {
				errs = append(errs, FieldError{Path: path, Reason: "v1 禁止声明插件间依赖（dependencies）"})
			} else if credentialKey.MatchString(k) {
				errs = append(errs, FieldError{Path: path, Reason: "v1 禁止在清单中出现凭据字段"})
			}
			errs = append(errs, scanForbiddenKeys(v[k], path)...)
		}
	case []any:
		for i, item := range v {
			errs = append(errs, scanForbiddenKeys(item, fmt.Sprintf("%s[%d]", prefix, i))...)
		}
	}
	return errs
}
