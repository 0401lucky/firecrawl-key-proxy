package keypool

import (
	"fmt"
	"time"

	"firecrawl-proxy/internal/store"
)

// Outcome 是一次上游请求的结果，由 C3 转发层构造并交给 Report。
type Outcome struct {
	StatusCode int           // 上游 HTTP 状态码；网络错误时无意义
	RetryAfter time.Duration // 从 429 响应的 Retry-After 头解析，0 表示缺失
	Err        error         // 网络层错误（连接失败/超时/DNS），非 HTTP 响应
}

// transition 是 classify 的产物：一次状态转移的完整描述。
// state 为空表示「不改变状态」；errMsg 为空表示「不写 last_error」。
type transition struct {
	state    store.KeyState
	cooldown time.Duration // state=cooling 时有效
	errMsg   string
}

// classify 把上游结果映射为状态转移。它是无副作用的纯函数，
// 全部状态判断集中于此，便于表驱动测试穷举。
//
// 关键边界：408/5xx 与网络错误（Outcome.Err != nil）不产生任何转移——
// 它们是 Firecrawl 自身的故障，不是 Key 的问题。误把这类结果计入惩罚，
// 会在上游抖动一次时把所有 Key 依次打成不可用，是这类代理最典型的故障模式。
func classify(o Outcome, defaultCooldown time.Duration) transition {
	if o.Err != nil {
		return transition{}
	}
	switch o.StatusCode {
	case 402:
		return transition{
			state:  store.StateExhausted,
			errMsg: "上游返回 402：套餐额度耗尽或未配置计费",
		}
	case 401:
		return transition{
			state:  store.StateInvalid,
			errMsg: "上游返回 401：Key 缺失、格式错误或已吊销",
		}
	case 403:
		return transition{
			state:  store.StateInvalid,
			errMsg: "上游返回 403：Key 权限不足",
		}
	case 429:
		cd := defaultCooldown
		if o.RetryAfter > 0 {
			cd = o.RetryAfter
		}
		return transition{
			state:    store.StateCooling,
			cooldown: cd,
			errMsg:   fmt.Sprintf("上游返回 429：触发限流，冷却 %s", cd),
		}
	default:
		// 2xx、其余 4xx、408、5xx 均不改变状态。
		return transition{}
	}
}

// Clock 是可注入的时间源。生产用 RealClock，测试用 fakeClock，
// 这是冷却时长与自动恢复能被确定性验证的前提。
type Clock interface {
	Now() time.Time
}

// RealClock 是生产时钟。
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
