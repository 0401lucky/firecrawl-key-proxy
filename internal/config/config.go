// Package config 负责从环境变量加载并校验全部配置项。
//
// 所有配置通过环境变量注入（便于容器化），默认值见各字段注释。
// 必填项（PUBLIC_BASE_URL、ADMIN_PASSWORD）缺失时返回聚合的错误列表，
// 一次告诉用户全部缺什么，而不是只报第一个。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 是运行时的全部配置，加载完成后视为只读。
type Config struct {
	ListenAddr           string   // 监听地址，默认 :8080
	PublicBaseURL        string   // 对外地址，用于响应体绝对 URL 重写（必填）
	DBPath               string   // SQLite 文件路径，默认 /data/proxy.db
	AdminPassword        string   // 面板管理员密码（必填）
	RegisterToken        string   // 自动注册器上传 Key 的共享 token（可选，空则接口返回 503）
	UpstreamBaseURL      string   // 上游 Firecrawl 地址，默认 https://api.firecrawl.dev
	ProxyPathPrefixes    []string // 需转发的路径前缀，默认 /v1/,/v2/
	MaxFailoverAttempts  int      // 单个请求最多尝试的 Key 数，默认 3
	DefaultCooldownSec   int64    // 429 无 Retry-After 时的冷却秒数，默认 60
	MaxRequestBufferSize int64    // 请求体缓冲上限，超过则不支持故障转移，默认 8 MiB
	JobRouteTTLHours     int      // job 映射保留时长，默认 48 小时
	CreditRefreshMinutes int      // 后台额度刷新间隔，0 表示关闭，默认 10
	SessionTTLHours      int      // 面板会话有效期，默认 168 小时（7 天）
	LogLevel             string   // 日志级别，默认 info
}

const (
	defaultListenAddr          = ":8080"
	defaultDBPath              = "/data/proxy.db"
	defaultUpstreamBaseURL     = "https://api.firecrawl.dev"
	defaultProxyPathPrefixes   = "/v1/,/v2/"
	defaultMaxFailoverAttempts = 3
	defaultCooldownSec         = 60
	defaultMaxRequestBuffer    = 8 * 1024 * 1024 // 8 MiB
	defaultJobRouteTTLHours    = 48
	defaultCreditRefreshMin    = 10
	defaultSessionTTLHours     = 168
	defaultLogLevel            = "info"
)

// Load 从环境变量读取配置：先填默认值，再逐项覆盖并校验。
// 返回的错误是聚合错误（errors.Join），包含所有校验失败项。
func Load() (*Config, error) {
	cfg := &Config{
		ListenAddr:           defaultListenAddr,
		DBPath:               defaultDBPath,
		UpstreamBaseURL:      defaultUpstreamBaseURL,
		MaxFailoverAttempts:  defaultMaxFailoverAttempts,
		DefaultCooldownSec:   defaultCooldownSec,
		MaxRequestBufferSize: defaultMaxRequestBuffer,
		JobRouteTTLHours:     defaultJobRouteTTLHours,
		CreditRefreshMinutes: defaultCreditRefreshMin,
		SessionTTLHours:      defaultSessionTTLHours,
		LogLevel:             defaultLogLevel,
	}

	var errs []error

	cfg.ListenAddr = getEnv("LISTEN_ADDR", cfg.ListenAddr)
	cfg.PublicBaseURL = getEnv("PUBLIC_BASE_URL", "")
	cfg.DBPath = getEnv("DB_PATH", cfg.DBPath)
	cfg.AdminPassword = getEnv("ADMIN_PASSWORD", "")
	cfg.RegisterToken = getEnv("REGISTER_TOKEN", "")
	cfg.UpstreamBaseURL = getEnv("UPSTREAM_BASE_URL", cfg.UpstreamBaseURL)
	cfg.LogLevel = getEnv("LOG_LEVEL", cfg.LogLevel)

	if v := os.Getenv("PROXY_PATH_PREFIXES"); v != "" {
		cfg.ProxyPathPrefixes = parsePrefixes(v)
	} else {
		cfg.ProxyPathPrefixes = parsePrefixes(defaultProxyPathPrefixes)
	}

	// 必填项检查：一次列出全部缺失项。
	if cfg.PublicBaseURL == "" {
		errs = append(errs, errors.New("缺少必填环境变量 PUBLIC_BASE_URL（对外地址，用于响应 URL 重写）"))
	}
	if cfg.AdminPassword == "" {
		errs = append(errs, errors.New("缺少必填环境变量 ADMIN_PASSWORD（面板管理员密码）"))
	}

	// 数值项解析：解析失败即为配置错误。
	if v := os.Getenv("MAX_FAILOVER_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("MAX_FAILOVER_ATTEMPTS=%q 必须是大于 0 的整数", v))
		} else {
			cfg.MaxFailoverAttempts = n
		}
	}
	if v := os.Getenv("DEFAULT_COOLDOWN_SECONDS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("DEFAULT_COOLDOWN_SECONDS=%q 必须是非负整数", v))
		} else {
			cfg.DefaultCooldownSec = n
		}
	}
	if v := os.Getenv("MAX_REQUEST_BUFFER_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("MAX_REQUEST_BUFFER_BYTES=%q 必须是非负整数", v))
		} else {
			cfg.MaxRequestBufferSize = n
		}
	}
	if v := os.Getenv("JOB_ROUTE_TTL_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("JOB_ROUTE_TTL_HOURS=%q 必须是大于 0 的整数", v))
		} else {
			cfg.JobRouteTTLHours = n
		}
	}
	if v := os.Getenv("CREDIT_REFRESH_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("CREDIT_REFRESH_MINUTES=%q 必须是非负整数", v))
		} else {
			cfg.CreditRefreshMinutes = n
		}
	}
	if v := os.Getenv("SESSION_TTL_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("SESSION_TTL_HOURS=%q 必须是大于 0 的整数", v))
		} else {
			cfg.SessionTTLHours = n
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parsePrefixes 把逗号分隔的前缀列表拆成切片并去掉空白，忽略空项。
func parsePrefixes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
