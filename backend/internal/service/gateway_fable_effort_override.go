package service

// fable-5 强制思考强度覆写（本地魔改，受保护补丁）。
//
// 背景：fable-5 是自适应思考模型，客户端不传 output_config.effort 时由模型自行
// 决定思考深度。本补丁允许通过环境变量把该模型的思考强度强制覆写为固定值。
//
// 开关：环境变量 SUB2API_FABLE_FORCE_EFFORT（在 /root/sub2api-modded.env 配置）
//   - 合法值：low / medium / high / xhigh / max
//   - 未设置、为空或非法值 = 功能完全关闭，转发行为与无此补丁时逐字节一致
//
// ===== 如何回撤 =====
// 1. 秒级回撤（不动代码）：编辑 /root/sub2api-modded.env，删除或注释
//    SUB2API_FABLE_FORCE_EFFORT 行，然后重启 sub2api-modded.service
//    （按运维 SOP 应经用户授权，或走 GitHub Actions deploy 流程重启）。
// 2. 彻底回撤（删代码）：revert 引入本补丁的 commit（见 docs/magic-patch-log.md
//    「fable-5 强制思考强度覆写」条目中登记的 commit id），push 触发 Actions
//    重新构建部署。涉及文件：本文件 + 同名 _test.go + gateway_forward.go 的
//    调用接缝（~4 行）+ gateway_service.go 的 env 读取字段（~3 行）。
// ====================

import (
	"os"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// fableForceEffortEnv is the env var holding the forced effort tier.
const fableForceEffortEnv = "SUB2API_FABLE_FORCE_EFFORT"

// ReadFableForceEffortFromEnv reads and validates the forced-effort setting.
// Returns "" when the feature is disabled (unset/empty/invalid value).
func ReadFableForceEffortFromEnv() string {
	raw := os.Getenv(fableForceEffortEnv)
	normalized := NormalizeClaudeOutputEffort(raw)
	if normalized == nil {
		return ""
	}
	return *normalized
}

// ForceFableOutputEffort unconditionally rewrites output_config.effort for
// fable-family models. When thinking is not already enabled/adaptive it also
// sets thinking.type=adaptive so the effort tier can take effect.
//
// effort=="" (feature off) or a non-fable model returns the body unchanged.
// Returns (body, true) when a rewrite was applied.
func ForceFableOutputEffort(body []byte, mappedModel, effort string) ([]byte, bool) {
	if effort == "" {
		return body, false
	}
	if !strings.Contains(strings.ToLower(mappedModel), "fable") {
		return body, false
	}

	changed := false

	if gjson.GetBytes(body, "output_config.effort").String() != effort {
		modified, err := sjson.SetBytes(body, "output_config.effort", effort)
		if err != nil {
			return body, false
		}
		body = modified
		changed = true
	}

	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType != "enabled" && thinkingType != "adaptive" {
		modified, err := sjson.SetBytes(body, "thinking.type", "adaptive")
		if err != nil {
			return body, changed
		}
		body = modified
		changed = true
	}

	return body, changed
}
