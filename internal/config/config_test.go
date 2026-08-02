package config

import (
	"strings"
	"testing"
)

// clearEnv 清掉本包相关的所有环境变量，保证每个用例从干净状态开始。
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"LISTEN_ADDR", "PUBLIC_BASE_URL", "DB_PATH", "ADMIN_PASSWORD",
		"UPSTREAM_BASE_URL", "PROXY_PATH_PREFIXES", "MAX_FAILOVER_ATTEMPTS",
		"DEFAULT_COOLDOWN_SECONDS", "MAX_REQUEST_BUFFER_BYTES",
		"JOB_ROUTE_TTL_HOURS", "CREDIT_REFRESH_MINUTES",
		"SESSION_TTL_HOURS", "LOG_LEVEL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "https://fc.example.com")
	t.Setenv("ADMIN_PASSWORD", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应报错: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("LISTEN_ADDR 默认值错误: got %q want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.DBPath != "/data/proxy.db" {
		t.Errorf("DB_PATH 默认值错误: got %q", cfg.DBPath)
	}
	if cfg.UpstreamBaseURL != "https://api.firecrawl.dev" {
		t.Errorf("UPSTREAM_BASE_URL 默认值错误: got %q", cfg.UpstreamBaseURL)
	}
	if len(cfg.ProxyPathPrefixes) != 2 || cfg.ProxyPathPrefixes[0] != "/v1/" || cfg.ProxyPathPrefixes[1] != "/v2/" {
		t.Errorf("PROXY_PATH_PREFIXES 默认值错误: got %v", cfg.ProxyPathPrefixes)
	}
	if cfg.MaxFailoverAttempts != 3 {
		t.Errorf("MAX_FAILOVER_ATTEMPTS 默认值错误: got %d", cfg.MaxFailoverAttempts)
	}
	if cfg.DefaultCooldownSec != 60 {
		t.Errorf("DEFAULT_COOLDOWN_SECONDS 默认值错误: got %d", cfg.DefaultCooldownSec)
	}
	if cfg.MaxRequestBufferSize != 8*1024*1024 {
		t.Errorf("MAX_REQUEST_BUFFER_BYTES 默认值错误: got %d", cfg.MaxRequestBufferSize)
	}
	if cfg.JobRouteTTLHours != 48 {
		t.Errorf("JOB_ROUTE_TTL_HOURS 默认值错误: got %d", cfg.JobRouteTTLHours)
	}
	if cfg.CreditRefreshMinutes != 10 {
		t.Errorf("CREDIT_REFRESH_MINUTES 默认值错误: got %d", cfg.CreditRefreshMinutes)
	}
	if cfg.SessionTTLHours != 168 {
		t.Errorf("SESSION_TTL_HOURS 默认值错误: got %d", cfg.SessionTTLHours)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LOG_LEVEL 默认值错误: got %q", cfg.LogLevel)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	clearEnv(t)

	_, err := Load()
	if err == nil {
		t.Fatal("两个必填项都缺失时 Load() 必须报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "PUBLIC_BASE_URL") {
		t.Errorf("错误信息应指出 PUBLIC_BASE_URL 缺失, got: %s", msg)
	}
	if !strings.Contains(msg, "ADMIN_PASSWORD") {
		t.Errorf("错误信息应指出 ADMIN_PASSWORD 缺失, got: %s", msg)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "https://fc.example.com")
	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("LISTEN_ADDR", "127.0.0.1:9090")
	t.Setenv("DB_PATH", "./test.db")
	t.Setenv("UPSTREAM_BASE_URL", "https://upstream.test")
	t.Setenv("PROXY_PATH_PREFIXES", "/v2/, /v3/")
	t.Setenv("MAX_FAILOVER_ATTEMPTS", "5")
	t.Setenv("DEFAULT_COOLDOWN_SECONDS", "120")
	t.Setenv("MAX_REQUEST_BUFFER_BYTES", "1048576")
	t.Setenv("JOB_ROUTE_TTL_HOURS", "24")
	t.Setenv("CREDIT_REFRESH_MINUTES", "0")
	t.Setenv("SESSION_TTL_HOURS", "24")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 不应报错: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9090" {
		t.Errorf("LISTEN_ADDR 覆盖失败: got %q", cfg.ListenAddr)
	}
	if cfg.DBPath != "./test.db" {
		t.Errorf("DB_PATH 覆盖失败: got %q", cfg.DBPath)
	}
	if cfg.UpstreamBaseURL != "https://upstream.test" {
		t.Errorf("UPSTREAM_BASE_URL 覆盖失败: got %q", cfg.UpstreamBaseURL)
	}
	if len(cfg.ProxyPathPrefixes) != 2 || cfg.ProxyPathPrefixes[0] != "/v2/" || cfg.ProxyPathPrefixes[1] != "/v3/" {
		t.Errorf("PROXY_PATH_PREFIXES 解析失败: got %v", cfg.ProxyPathPrefixes)
	}
	if cfg.MaxFailoverAttempts != 5 {
		t.Errorf("MAX_FAILOVER_ATTEMPTS 覆盖失败: got %d", cfg.MaxFailoverAttempts)
	}
	if cfg.DefaultCooldownSec != 120 {
		t.Errorf("DEFAULT_COOLDOWN_SECONDS 覆盖失败: got %d", cfg.DefaultCooldownSec)
	}
	if cfg.MaxRequestBufferSize != 1048576 {
		t.Errorf("MAX_REQUEST_BUFFER_BYTES 覆盖失败: got %d", cfg.MaxRequestBufferSize)
	}
	if cfg.CreditRefreshMinutes != 0 {
		t.Errorf("CREDIT_REFRESH_MINUTES 覆盖失败: got %d", cfg.CreditRefreshMinutes)
	}
}

func TestLoadInvalidNumbers(t *testing.T) {
	clearEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "https://fc.example.com")
	t.Setenv("ADMIN_PASSWORD", "secret")
	t.Setenv("MAX_FAILOVER_ATTEMPTS", "abc")
	t.Setenv("DEFAULT_COOLDOWN_SECONDS", "-5")
	t.Setenv("CREDIT_REFRESH_MINUTES", "-1")

	_, err := Load()
	if err == nil {
		t.Fatal("数值项非法时 Load() 必须报错")
	}
	for _, want := range []string{"MAX_FAILOVER_ATTEMPTS", "DEFAULT_COOLDOWN_SECONDS", "CREDIT_REFRESH_MINUTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应包含 %s, got: %s", want, err.Error())
		}
	}
}
