package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// openTestDB 在 t.TempDir() 下开一个真实 SQLite 库，返回可用的 Store。
func openTestDB(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() 失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("解析时间 %q 失败: %v", s, err)
	}
	return tm
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatalf("第一次 Open() 失败: %v", err)
	}
	// 写一条数据，验证第二次 Open 后仍在。
	_, err = db1.Exec(
		"INSERT INTO upstream_keys (name, api_key, key_suffix, created_at) VALUES (?, ?, ?, ?)",
		"test", "fc-abc", "abc", time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("插入测试数据失败: %v", err)
	}
	db1.Close()

	// 第二次打开同一文件：不报错、不重复建表、数据保留。
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("第二次 Open() 失败: %v", err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM upstream_keys").Scan(&count); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if count != 1 {
		t.Errorf("第二次打开后数据应保留, got %d 行, want 1", count)
	}

	// 四张表都应存在。
	for _, table := range []string{"upstream_keys", "proxy_keys", "job_routes", "admin_sessions"} {
		var n int
		err := db2.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&n)
		if err != nil {
			t.Fatalf("检查表 %s 失败: %v", table, err)
		}
		if n != 1 {
			t.Errorf("表 %s 应存在, got %d", table, n)
		}
	}
}

func TestUpstreamKeyRepo(t *testing.T) {
	st := openTestDB(t)
	repo := st.UpstreamKeys

	// Create：自动派生 key_suffix。
	uk := &UpstreamKey{Name: "账号 A", APIKey: "fc-1234567890abcd", State: StateAvailable}
	id, err := repo.Create(uk)
	if err != nil {
		t.Fatalf("Create() 失败: %v", err)
	}
	if id == 0 {
		t.Error("Create() 应返回非零 id")
	}
	if uk.KeySuffix != "abcd" {
		t.Errorf("key_suffix 应自动派生为 abcd, got %q", uk.KeySuffix)
	}

	// Get。
	got, err := repo.Get(id)
	if err != nil {
		t.Fatalf("Get() 失败: %v", err)
	}
	if got.Name != "账号 A" || got.APIKey != "fc-1234567890abcd" {
		t.Errorf("Get() 返回内容不符: %+v", got)
	}
	if !got.Enabled {
		t.Error("enabled 默认应为 true")
	}
	if got.State != StateAvailable {
		t.Errorf("state 默认应为 available, got %q", got.State)
	}

	// Get 不存在的 id → sql.ErrNoRows。
	if _, err := repo.Get(99999); err != sql.ErrNoRows {
		t.Errorf("Get(不存在) 应返回 sql.ErrNoRows, got %v", err)
	}

	// Update：state → exhausted，写 last_error 与 cooldown。
	cooldown := mustTime(t, "2026-08-03T10:00:00+08:00")
	lastErr := "402 额度耗尽"
	uk.State = StateExhausted
	uk.CooldownUntil = &cooldown
	uk.LastError = &lastErr
	if err := repo.Update(uk); err != nil {
		t.Fatalf("Update() 失败: %v", err)
	}
	got, err = repo.Get(id)
	if err != nil {
		t.Fatalf("Get() 失败: %v", err)
	}
	if got.State != StateExhausted {
		t.Errorf("Update 后 state 应为 exhausted, got %q", got.State)
	}
	if got.CooldownUntil == nil || !got.CooldownUntil.Equal(cooldown) {
		t.Errorf("Update 后 cooldown_until 不符: %v", got.CooldownUntil)
	}
	if got.LastError == nil || *got.LastError != "402 额度耗尽" {
		t.Errorf("Update 后 last_error 不符: %v", got.LastError)
	}

	// Update 置空可空字段。
	uk.CooldownUntil = nil
	uk.LastError = nil
	if err := repo.Update(uk); err != nil {
		t.Fatalf("Update() 置空失败: %v", err)
	}
	got, _ = repo.Get(id)
	if got.CooldownUntil != nil || got.LastError != nil {
		t.Errorf("Update 后可空字段应置空, got cooldown=%v last_error=%v", got.CooldownUntil, got.LastError)
	}

	// IncrementUsage 批量累加。
	if err := repo.IncrementUsage(map[int64]int64{id: 5}); err != nil {
		t.Fatalf("IncrementUsage() 失败: %v", err)
	}
	if err := repo.IncrementUsage(map[int64]int64{id: 2}); err != nil {
		t.Fatalf("IncrementUsage() 失败: %v", err)
	}
	got, _ = repo.Get(id)
	if got.RequestCount != 7 {
		t.Errorf("request_count 应为 7, got %d", got.RequestCount)
	}
	if got.LastUsedAt == nil {
		t.Error("last_used_at 应被置为当前时间")
	}
	// 空 map 与非法 delta 不应报错。
	if err := repo.IncrementUsage(map[int64]int64{}); err != nil {
		t.Errorf("空 map 不应报错: %v", err)
	}
	if err := repo.IncrementUsage(map[int64]int64{id: 0}); err != nil {
		t.Errorf("零 delta 不应报错: %v", err)
	}

	// List。
	all, err := repo.List()
	if err != nil {
		t.Fatalf("List() 失败: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List() 应有 1 条, got %d", len(all))
	}

	// Delete。
	if err := repo.Delete(id); err != nil {
		t.Fatalf("Delete() 失败: %v", err)
	}
	if _, err := repo.Get(id); err != sql.ErrNoRows {
		t.Errorf("Delete 后 Get 应返回 sql.ErrNoRows, got %v", err)
	}
}

func TestUpstreamKeyUniqueConstraint(t *testing.T) {
	st := openTestDB(t)
	repo := st.UpstreamKeys

	if _, err := repo.Create(&UpstreamKey{Name: "a", APIKey: "fc-same"}); err != nil {
		t.Fatalf("第一次 Create() 失败: %v", err)
	}
	if _, err := repo.Create(&UpstreamKey{Name: "b", APIKey: "fc-same"}); err == nil {
		t.Error("重复 api_key 应触发 UNIQUE 约束报错")
	}
}

func TestProxyKeyRepo(t *testing.T) {
	st := openTestDB(t)
	repo := st.ProxyKeys

	pk := &ProxyKey{Name: "调用方 A", KeyHash: "abc123hash", KeyPrefix: "fcp_abcdef1234"}
	id, err := repo.Create(pk)
	if err != nil {
		t.Fatalf("Create() 失败: %v", err)
	}

	// FindByHash。
	got, err := repo.FindByHash("abc123hash")
	if err != nil {
		t.Fatalf("FindByHash() 失败: %v", err)
	}
	if got.Name != "调用方 A" || got.KeyPrefix != "fcp_abcdef1234" {
		t.Errorf("FindByHash 返回内容不符: %+v", got)
	}
	if got.Revoked {
		t.Error("新 Key 不应是吊销状态")
	}
	// 不存在的哈希 → sql.ErrNoRows。
	if _, err := repo.FindByHash("nope"); err != sql.ErrNoRows {
		t.Errorf("FindByHash(不存在) 应返回 sql.ErrNoRows, got %v", err)
	}

	// List。
	all, err := repo.List()
	if err != nil {
		t.Fatalf("List() 失败: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("List() 应有 1 条, got %d", len(all))
	}

	// IncrementUsage。
	if err := repo.IncrementUsage(map[int64]int64{id: 3}); err != nil {
		t.Fatalf("IncrementUsage() 失败: %v", err)
	}
	got, _ = repo.FindByHash("abc123hash")
	if got.RequestCount != 3 {
		t.Errorf("request_count 应为 3, got %d", got.RequestCount)
	}

	// Revoke。
	if err := repo.Revoke(id); err != nil {
		t.Fatalf("Revoke() 失败: %v", err)
	}
	got, _ = repo.FindByHash("abc123hash")
	if !got.Revoked {
		t.Error("Revoke 后 revoked 应为 true")
	}
}

func TestJobRouteRepo(t *testing.T) {
	st := openTestDB(t)

	// 先造一个上游 Key，验证外键与级联删除。
	ukID, err := st.UpstreamKeys.Create(&UpstreamKey{Name: "k", APIKey: "fc-route"})
	if err != nil {
		t.Fatalf("创建上游 Key 失败: %v", err)
	}

	// 存储以 unix 秒为单位，比较基准先截断到秒，避免亚秒精度导致的 Equal 失败。
	now := time.Now().Truncate(time.Second)
	jr := &JobRoute{
		JobID:         "job-1",
		UpstreamKeyID: ukID,
		Kind:          "crawl",
		CreatedAt:     now,
		ExpiresAt:     now.Add(48 * time.Hour),
	}
	if err := st.JobRoutes.Upsert(jr); err != nil {
		t.Fatalf("Upsert() 失败: %v", err)
	}

	got, err := st.JobRoutes.Get("job-1")
	if err != nil {
		t.Fatalf("Get() 失败: %v", err)
	}
	if got.UpstreamKeyID != ukID || got.Kind != "crawl" {
		t.Errorf("Get 返回内容不符: %+v", got)
	}
	if !got.ExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Errorf("expires_at 不符: %v", got.ExpiresAt)
	}
	// 不存在的 job → sql.ErrNoRows。
	if _, err := st.JobRoutes.Get("nope"); err != sql.ErrNoRows {
		t.Errorf("Get(不存在) 应返回 sql.ErrNoRows, got %v", err)
	}

	// Upsert 覆盖同一条。
	newExpiry := now.Add(72 * time.Hour)
	jr.ExpiresAt = newExpiry
	if err := st.JobRoutes.Upsert(jr); err != nil {
		t.Fatalf("Upsert() 覆盖失败: %v", err)
	}
	got, _ = st.JobRoutes.Get("job-1")
	if !got.ExpiresAt.Equal(newExpiry) {
		t.Errorf("Upsert 覆盖后 expires_at 不符: %v", got.ExpiresAt)
	}

	// DeleteExpired：只删过期的。
	if err := st.JobRoutes.Upsert(&JobRoute{
		JobID: "job-expired", UpstreamKeyID: ukID, Kind: "crawl",
		CreatedAt: now, ExpiresAt: now.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("Upsert() 失败: %v", err)
	}
	n, err := st.JobRoutes.DeleteExpired(now)
	if err != nil {
		t.Fatalf("DeleteExpired() 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteExpired 应删除 1 条, got %d", n)
	}
	if _, err := st.JobRoutes.Get("job-expired"); err != sql.ErrNoRows {
		t.Errorf("过期 job 应被删除, got %v", err)
	}
	if _, err := st.JobRoutes.Get("job-1"); err != nil {
		t.Errorf("未过期 job 应保留: %v", err)
	}

	// 删除上游 Key 后，其 job 映射经外键级联删除。
	if err := st.UpstreamKeys.Delete(ukID); err != nil {
		t.Fatalf("删除上游 Key 失败: %v", err)
	}
	if _, err := st.JobRoutes.Get("job-1"); err != sql.ErrNoRows {
		t.Errorf("级联删除后 job-1 应不存在, got %v", err)
	}
}

func TestSessionRepo(t *testing.T) {
	st := openTestDB(t)
	repo := st.Sessions

	now := time.Now().Truncate(time.Second)
	s := &Session{TokenHash: "tok-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := repo.Create(s); err != nil {
		t.Fatalf("Create() 失败: %v", err)
	}

	got, err := repo.Get("tok-1")
	if err != nil {
		t.Fatalf("Get() 失败: %v", err)
	}
	if !got.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("expires_at 不符: %v", got.ExpiresAt)
	}
	if _, err := repo.Get("nope"); err != sql.ErrNoRows {
		t.Errorf("Get(不存在) 应返回 sql.ErrNoRows, got %v", err)
	}

	// 过期清理。
	if err := repo.Create(&Session{TokenHash: "tok-expired", CreatedAt: now, ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatalf("Create() 失败: %v", err)
	}
	n, err := repo.DeleteExpired(now)
	if err != nil {
		t.Fatalf("DeleteExpired() 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteExpired 应删除 1 条, got %d", n)
	}

	// Delete。
	if err := repo.Delete("tok-1"); err != nil {
		t.Fatalf("Delete() 失败: %v", err)
	}
	if _, err := repo.Get("tok-1"); err != sql.ErrNoRows {
		t.Errorf("Delete 后应查不到, got %v", err)
	}
}
