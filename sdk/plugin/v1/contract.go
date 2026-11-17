// 契约常量：插件与底座共享的契约版本、错误类别与命令式退出码。
// 事实来源：plugin-spec/05-errors-and-exit-codes.md；本文件与该文档必须同步修订（ADR-0020、ADR-0024）。
// SDK 只含 proto、生成码与契约常量，不得 import 底座内部包（plugin-spec/03 §9）。
package pluginv1

// ContractVersion 是底座当前实现的契约版本（MAJOR.MINOR）。
// 插件在清单 version.contract 声明兼容区间；新增可选字段 = MINOR，改名/删除/改语义 = MAJOR
// （plugin-spec/07）。daemon 插件经 GetVersion 上报的实际值必须落在声明区间内。
const ContractVersion = "1.2"

// 错误类别（plugin-spec/05 §2）。插件不得自造未登记的类别；
// 底座对未登记类别归一为 internal_error 并保留原码（error_category_original）。
// 标注 1.1 的类别要求清单 version.contract 下限 >=1.1，否则被归一为 internal_error 并告警。
const (
	CodeInputInvalid          = "input_invalid"          // 1.0 输入结构不合法（缺必填、类型错、枚举越界）
	CodeCapabilityUnknown     = "capability_unknown"     // 1.0 调用了清单未声明的能力
	CodePermissionDenied      = "permission_denied"      // 1.0 访问未声明的资源类别/动作
	CodeNotReady              = "not_ready"              // 1.0 常驻插件尚未完成初始化
	CodeTimeout               = "timeout"                // 1.0 超过 deadline_ms
	CodeCancelled             = "cancelled"              // 1.0 调用方取消
	CodeResultTooLarge        = "result_too_large"       // 1.0 结果超限；禁止截断后返回成功
	CodeDependencyUnavailable = "dependency_unavailable" // 1.0 外部依赖不可用
	CodeInternalError         = "internal_error"         // 1.0 未预期异常
	CodeProtocolInvalid       = "protocol_invalid"       // 1.0 协议违规（stdout 混日志、响应缺字段）
	CodeInvalidResponse       = "invalid_response"       // 1.0 响应结构不合法（输出不符 schema）
	CodeVersionIncompatible   = "version_incompatible"   // 1.0 声明范围与底座无交集
	CodeNotFound              = "not_found"              // 1.1 目标对象不存在（不是能力不存在）
	CodeValidationFailed      = "validation_failed"      // 1.1 结构合法但业务校验不通过
	CodePreconditionFailed    = "precondition_failed"    // 1.1 前置条件不满足（状态、顺序、锁）
)

// 命令式插件退出码（plugin-spec/05 §3）。固定 0~8 共 9 个值，不新增 9 及以上；
// 退出码必须与响应 error.code 同桶，矛盾时底座按失败处理并记「协议不一致」。
// 退出码只是粗分类，精确原因以 error.code 为准。常驻插件无退出码语义（ADR-0019 §3.1）。
const (
	ExitOK                = 0 // 成功：status=ok
	ExitInternalError     = 1 // internal_error
	ExitInputInvalid      = 2 // input_invalid、validation_failed、not_found
	ExitCapabilityUnknown = 3 // capability_unknown
	ExitPermissionDenied  = 4 // permission_denied
	ExitTimeout           = 5 // timeout
	ExitResultTooLarge    = 6 // result_too_large
	ExitDependencyFailed  = 7 // dependency_unavailable、precondition_failed
	ExitProtocolInvalid   = 8 // protocol_invalid、invalid_response
)

// 响应 status 的合法取值（stdio 响应字段与 InvokeResponse.status 共用）。
const (
	StatusOK    = "ok"
	StatusError = "error"
)
