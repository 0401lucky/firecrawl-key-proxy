// Package logging 提供全项目统一的结构化日志与上游 Key 脱敏。
//
// 日志基于标准库 log/slog，输出 JSON，级别由 LOG_LEVEL 控制。
// 脱敏规则全项目只有这一份实现：MaskKey 一律输出 fc-**** + 末 4 位，
// 上游 Key 明文在任何日志与 API 输出中都不出现（父任务 prd R7.3 / AC11）。
package logging

import (
	"fmt"
	"log/slog"
	"os"
)

// New 构造 JSON 格式的 slog.Logger。level 取 debug/info/warn/error，
// 非法值时返回错误，由调用方决定是否终止——不静默降级。
func New(level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("非法的日志级别 %q（可选 debug/info/warn/error）: %w", level, err)
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h), nil
}

// MaskKey 把上游 Key 转成展示用脱敏形式：fc-**** + 末 4 位。
//
//	MaskKey("fc-1234567890abcd") -> "fc-****abcd"
//
// 输入长度不足 4 时整体掩掉（返回 fc-****），不 panic、不泄漏短密钥。
// 注意：这不是哈希，仅用于日志与面板展示；真正的防回读依赖数据库权限控制。
func MaskKey(key string) string {
	if len(key) <= 4 {
		return "fc-****"
	}
	return "fc-****" + key[len(key)-4:]
}
