package store

import "time"

// KeyState 是上游 Key 的状态，具名字符串类型。
// 状态机语义与转移规则见 C2（keypool），本处只定义取值常量，C2 直接复用。
type KeyState string

const (
	StateAvailable KeyState = "available" // 可用，可被轮询选中
	StateCooling   KeyState = "cooling"   // 429 触发的临时冷却，cooldown_until 到期自动恢复
	StateExhausted KeyState = "exhausted" // 402 触发，终态，需管理员手动重置
	StateInvalid   KeyState = "invalid"   // 401/403 触发，终态，需管理员手动重置
)

// UpstreamKey 是上游 Firecrawl Key 的领域结构体。
// APIKey 以明文入库（代理转发必需），但任何日志/API 输出都不得泄漏，见 logging.MaskKey。
type UpstreamKey struct {
	ID               int64
	Name             string
	APIKey           string `json:"-"` // 明文，仅存储与转发用，禁止序列化泄漏
	KeySuffix        string // 末 4 位，展示用
	Enabled          bool
	State            KeyState
	CooldownUntil    *time.Time // state=cooling 时有效
	CreditsTotal     *int64
	CreditsRemaining *int64
	CreditsSyncedAt  *time.Time
	RequestCount     int64
	LastError        *string
	LastUsedAt       *time.Time
	CreatedAt        time.Time
}

// ProxyKey 是下游代理 API Key 的领域结构体。明文只在创建时展示一次，
// 库里只存 sha256 哈希与前 12 位前缀。
type ProxyKey struct {
	ID           int64
	Name         string
	KeyHash      string // sha256(明文) 的十六进制
	KeyPrefix    string // 明文前 12 位，展示用
	Revoked      bool
	RequestCount int64
	LastUsedAt   *time.Time
	CreatedAt    time.Time
}

// JobRoute 记录异步任务与提交它的上游 Key 的粘连关系。
type JobRoute struct {
	JobID         string
	UpstreamKeyID int64
	Kind          string // crawl | batch_scrape | extract
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// Session 是面板管理员会话。
type Session struct {
	TokenHash string
	CreatedAt time.Time
	ExpiresAt time.Time
}
