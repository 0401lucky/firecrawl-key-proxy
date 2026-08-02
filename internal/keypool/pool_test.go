package keypool

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"firecrawl-proxy/internal/store"
)

// fakeClock 是可推进的测试时钟：冷却时长与自动恢复的确定性验证依赖它。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// setupPool 建一个临时 DB，写入 n 个可用 Key，返回池与仓储引用。
func setupPool(t *testing.T, n int) (*Pool, *store.Store, *fakeClock) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)

	for i := 1; i <= n; i++ {
		if _, err := st.UpstreamKeys.Create(&store.UpstreamKey{
			Name:   fmt.Sprintf("key-%d", i),
			APIKey: fmt.Sprintf("fc-test-%02d", i),
		}); err != nil {
			t.Fatalf("创建测试 Key %d 失败: %v", i, err)
		}
	}

	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	pool, err := New(st.UpstreamKeys, clock, Config{
		DefaultCooldown: 60 * time.Second,
		FlushInterval:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() 失败: %v", err)
	}
	return pool, st, clock
}

func mustGet(t *testing.T, st *store.Store, id int64) *store.UpstreamKey {
	t.Helper()
	uk, err := st.UpstreamKeys.Get(id)
	if err != nil {
		t.Fatalf("读取 Key %d 失败: %v", id, err)
	}
	return uk
}

// nextID 取出 Next() 返回的 Key 的 id；失败则 t.Fatal。
func nextID(t *testing.T, p *Pool) int64 {
	t.Helper()
	uk, err := p.Next()
	if err != nil {
		t.Fatalf("Next() 失败: %v", err)
	}
	return uk.ID
}

// ---- classify 表驱动 ----

func TestClassify(t *testing.T) {
	defaultCd := 60 * time.Second
	netErr := errors.New("连接被拒")
	cases := []struct {
		name    string
		outcome Outcome
		want    store.KeyState // 空 = 不改变状态
		wantCd  time.Duration  // 仅 cooling 断言
		wantMsg bool           // 是否写入 last_error
	}{
		{"2xx 成功", Outcome{StatusCode: 200}, "", 0, false},
		{"普通 4xx 客户端错误", Outcome{StatusCode: 400}, "", 0, false},
		{"401 永久失效", Outcome{StatusCode: 401}, store.StateInvalid, 0, true},
		{"402 额度耗尽", Outcome{StatusCode: 402}, store.StateExhausted, 0, true},
		{"403 权限不足", Outcome{StatusCode: 403}, store.StateInvalid, 0, true},
		{"408 上游超时——不惩罚", Outcome{StatusCode: 408}, "", 0, false},
		{"429 带头冷却", Outcome{StatusCode: 429, RetryAfter: 30 * time.Second}, store.StateCooling, 30 * time.Second, true},
		{"429 无头用默认", Outcome{StatusCode: 429}, store.StateCooling, defaultCd, true},
		{"500 上游故障——不惩罚", Outcome{StatusCode: 500}, "", 0, false},
		{"502 上游故障——不惩罚", Outcome{StatusCode: 502}, "", 0, false},
		{"网络错误——不惩罚", Outcome{Err: netErr}, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := classify(c.outcome, defaultCd)
			if tr.state != c.want {
				t.Errorf("classify(%+v).state = %q, want %q", c.outcome, tr.state, c.want)
			}
			if tr.state == store.StateCooling && tr.cooldown != c.wantCd {
				t.Errorf("cooldown = %v, want %v", tr.cooldown, c.wantCd)
			}
			if (tr.errMsg != "") != c.wantMsg {
				t.Errorf("errMsg 写状态 = %v, want %v (msg=%q)", tr.errMsg != "", c.wantMsg, tr.errMsg)
			}
		})
	}
}

// ---- 选择 ----

func TestRoundRobin(t *testing.T) {
	pool, _, _ := setupPool(t, 3)
	want := []int64{1, 2, 3, 1, 2, 3}
	for i, w := range want {
		if got := nextID(t, pool); got != w {
			t.Errorf("第 %d 次 Next() = %d, want %d", i+1, got, w)
		}
	}
}

func TestDisabledKeyNeverSelected(t *testing.T) {
	pool, st, _ := setupPool(t, 3)
	// 禁用 2 号 Key。
	uk := mustGet(t, st, 2)
	uk.Enabled = false
	if err := st.UpstreamKeys.Update(uk); err != nil {
		t.Fatalf("禁用 Key 失败: %v", err)
	}
	pool.Reload()

	// 连续选 6 次，2 号永不出现。
	for i := 0; i < 6; i++ {
		if got := nextID(t, pool); got == 2 {
			t.Fatalf("第 %d 次 Next() 返回了被禁用的 Key 2", i+1)
		}
	}
}

func TestNoKeyAvailable(t *testing.T) {
	pool, _, _ := setupPool(t, 2)
	// 全部标记 exhausted。
	pool.Report(1, Outcome{StatusCode: 402})
	pool.Report(2, Outcome{StatusCode: 402})

	if _, err := pool.Next(); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("Next() = %v, want ErrNoKeyAvailable", err)
	}
}

func TestNextExcluding(t *testing.T) {
	pool, _, _ := setupPool(t, 3)

	// 排除 1、2 → 只可能返回 3。
	for i := 0; i < 3; i++ {
		uk, err := pool.NextExcluding([]int64{1, 2})
		if err != nil {
			t.Fatalf("NextExcluding() 失败: %v", err)
		}
		if uk.ID != 3 {
			t.Errorf("NextExcluding([1,2]) = %d, want 3", uk.ID)
		}
	}

	// 排除全部 → ErrNoKeyAvailable。
	if _, err := pool.NextExcluding([]int64{1, 2, 3}); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("NextExcluding([1,2,3]) = %v, want ErrNoKeyAvailable", err)
	}
}

// ---- 状态转移 ----

func TestReportExhausted(t *testing.T) {
	pool, st, _ := setupPool(t, 2)

	pool.Report(1, Outcome{StatusCode: 402})

	// 内存：1 号不再被选中。
	for i := 0; i < 3; i++ {
		if got := nextID(t, pool); got == 1 {
			t.Fatalf("第 %d 次 Next() 返回了已耗尽的 Key 1", i+1)
		}
	}
	// DB：state 已落库，last_error 已写。
	uk := mustGet(t, st, 1)
	if uk.State != store.StateExhausted {
		t.Errorf("DB state = %q, want exhausted", uk.State)
	}
	if uk.LastError == nil || *uk.LastError == "" {
		t.Error("last_error 应已写入")
	}
	// 另一 Key 不受影响。
	uk2 := mustGet(t, st, 2)
	if uk2.State != store.StateAvailable {
		t.Errorf("Key 2 state = %q, want available", uk2.State)
	}
}

func TestReportInvalid(t *testing.T) {
	pool, st, _ := setupPool(t, 1)
	pool.Report(1, Outcome{StatusCode: 401})

	uk := mustGet(t, st, 1)
	if uk.State != store.StateInvalid {
		t.Errorf("401 后 state = %q, want invalid", uk.State)
	}
	if _, err := pool.Next(); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("401 后 Next() = %v, want ErrNoKeyAvailable", err)
	}
}

func TestReportCoolingWithRetryAfter(t *testing.T) {
	pool, st, clock := setupPool(t, 1)

	pool.Report(1, Outcome{StatusCode: 429, RetryAfter: 30 * time.Second})

	// 冷却中：Snapshot 剩余约 30 秒，Next 不返回。
	keys, counts := pool.Snapshot()
	if counts[store.StateCooling] != 1 {
		t.Errorf("counts[cooling] = %d, want 1", counts[store.StateCooling])
	}
	if rem := keys[0].CooldownRemaining; rem < 29 || rem > 30 {
		t.Errorf("冷却剩余 = %d 秒, want 约 30", rem)
	}
	if _, err := pool.Next(); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("冷却中 Next() = %v, want ErrNoKeyAvailable", err)
	}
	// DB 已落库。
	uk := mustGet(t, st, 1)
	if uk.State != store.StateCooling {
		t.Errorf("DB state = %q, want cooling", uk.State)
	}

	// 推进 31 秒 → 惰性恢复。
	clock.Advance(31 * time.Second)
	if got := nextID(t, pool); got != 1 {
		t.Errorf("恢复后 Next() = %d, want 1", got)
	}
	keys, counts = pool.Snapshot()
	if counts[store.StateAvailable] != 1 {
		t.Errorf("恢复后 counts[available] = %d, want 1", counts[store.StateAvailable])
	}
	if keys[0].CooldownRemaining != 0 {
		t.Errorf("恢复后冷却剩余应为 0, got %d", keys[0].CooldownRemaining)
	}
	// DB 中的恢复也应已落库（AC10 重启后仍可用）。
	uk = mustGet(t, st, 1)
	if uk.State != store.StateAvailable || uk.CooldownUntil != nil {
		t.Errorf("恢复后 DB state = %q cooldown=%v, want available + nil", uk.State, uk.CooldownUntil)
	}
}

func TestReportCoolingDefaultCooldown(t *testing.T) {
	pool, _, clock := setupPool(t, 1)

	pool.Report(1, Outcome{StatusCode: 429}) // 无 Retry-After

	keys, _ := pool.Snapshot()
	if rem := keys[0].CooldownRemaining; rem < 59 || rem > 60 {
		t.Errorf("无头冷却剩余 = %d 秒, want 约 60", rem)
	}

	// 推进 59 秒仍在冷却，61 秒恢复。
	clock.Advance(59 * time.Second)
	if _, err := pool.Next(); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("59 秒后 Next() = %v, want ErrNoKeyAvailable", err)
	}
	clock.Advance(2 * time.Second)
	if got := nextID(t, pool); got != 1 {
		t.Errorf("61 秒后 Next() = %d, want 1", got)
	}
}

// TestReportUpstreamFaultKeepsState 是全局最重要的回归测试：
// 408/5xx/网络错误绝不惩罚 Key——Firecrawl 自身抖动不能把好 Key 打成不可用。
func TestReportUpstreamFaultKeepsState(t *testing.T) {
	faults := []Outcome{
		{StatusCode: 408},
		{StatusCode: 500},
		{StatusCode: 502},
		{StatusCode: 503},
		{Err: errors.New("连接被拒")},
		{Err: errors.New("读取响应超时")},
	}
	for _, o := range faults {
		name := fmt.Sprintf("fault-%d-%v", o.StatusCode, o.Err != nil)
		t.Run(name, func(t *testing.T) {
			pool, st, _ := setupPool(t, 1)

			pool.Report(1, o)

			uk := mustGet(t, st, 1)
			if uk.State != store.StateAvailable {
				t.Errorf("故障 %+v 后 DB state = %q, want available（不得惩罚）", o, uk.State)
			}
			if uk.LastError != nil {
				t.Errorf("故障 %+v 后不应写 last_error, got %q", o, *uk.LastError)
			}
			// Key 仍可被选中。
			if got := nextID(t, pool); got != 1 {
				t.Errorf("故障后 Next() = %d, want 1", got)
			}
		})
	}
}

func TestSnapshotCounts(t *testing.T) {
	pool, _, _ := setupPool(t, 3)
	pool.Report(1, Outcome{StatusCode: 402})
	pool.Report(2, Outcome{StatusCode: 429, RetryAfter: time.Minute})

	_, counts := pool.Snapshot()
	if counts[store.StateExhausted] != 1 {
		t.Errorf("counts[exhausted] = %d, want 1", counts[store.StateExhausted])
	}
	if counts[store.StateCooling] != 1 {
		t.Errorf("counts[cooling] = %d, want 1", counts[store.StateCooling])
	}
	if counts[store.StateAvailable] != 1 {
		t.Errorf("counts[available] = %d, want 1", counts[store.StateAvailable])
	}
}

// Snapshot 对已过期的冷却做惰性恢复（面板轮询即可看到真实状态，无需等请求触发）。
func TestSnapshotLazyRecoversExpiredCooling(t *testing.T) {
	pool, st, clock := setupPool(t, 1)

	pool.Report(1, Outcome{StatusCode: 429, RetryAfter: 10 * time.Second})
	clock.Advance(11 * time.Second) // 冷却已过期，但无任何请求触发 next()

	keys, counts := pool.Snapshot()
	if counts[store.StateCooling] != 0 {
		t.Errorf("过期冷却 counts[cooling] = %d, want 0", counts[store.StateCooling])
	}
	if counts[store.StateAvailable] != 1 {
		t.Errorf("过期冷却 counts[available] = %d, want 1", counts[store.StateAvailable])
	}
	if keys[0].CooldownRemaining != 0 {
		t.Errorf("过期冷却剩余 = %d, want 0", keys[0].CooldownRemaining)
	}
	// DB 已同步落库。
	uk := mustGet(t, st, 1)
	if uk.State != store.StateAvailable || uk.CooldownUntil != nil {
		t.Errorf("Snapshot 恢复后 DB state = %q cooldown=%v, want available + nil", uk.State, uk.CooldownUntil)
	}
}

// ---- Reload ----

func TestReloadPicksUpNewKey(t *testing.T) {
	pool, st, _ := setupPool(t, 2)

	// 面板新增一个 Key（直接写 DB），Reload 后应可被选中。
	newID, err := st.UpstreamKeys.Create(&store.UpstreamKey{Name: "new", APIKey: "fc-new-key-99"})
	if err != nil {
		t.Fatalf("新增 Key 失败: %v", err)
	}
	if err := pool.Reload(); err != nil {
		t.Fatalf("Reload() 失败: %v", err)
	}

	found := false
	for i := 0; i < 10; i++ {
		if nextID(t, pool) == newID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Reload 后新 Key 从未被选中")
	}
}

func TestReloadAfterDelete(t *testing.T) {
	pool, st, _ := setupPool(t, 2)
	if err := st.UpstreamKeys.Delete(2); err != nil {
		t.Fatalf("删除 Key 失败: %v", err)
	}
	if err := pool.Reload(); err != nil {
		t.Fatalf("Reload() 失败: %v", err)
	}
	// 删除的 Key 不再返回；对已删除 Key 的 Report 静默丢弃。
	pool.Report(2, Outcome{StatusCode: 402})
	for i := 0; i < 4; i++ {
		if got := nextID(t, pool); got != 1 {
			t.Errorf("删除后 Next() = %d, want 1", got)
		}
	}
}

// ---- 用量刷盘 ----

func TestFlush(t *testing.T) {
	pool, st, _ := setupPool(t, 2)

	pool.RecordUsage(1)
	pool.RecordUsage(1)
	pool.RecordUsage(2)

	if err := pool.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	if got := mustGet(t, st, 1).RequestCount; got != 2 {
		t.Errorf("Key 1 request_count = %d, want 2", got)
	}
	if got := mustGet(t, st, 2).RequestCount; got != 1 {
		t.Errorf("Key 2 request_count = %d, want 1", got)
	}

	// 重复 Flush 不重复累加。
	if err := pool.Flush(); err != nil {
		t.Fatalf("第二次 Flush() 失败: %v", err)
	}
	if got := mustGet(t, st, 1).RequestCount; got != 2 {
		t.Errorf("重复 Flush 后 request_count = %d, want 仍为 2", got)
	}
}

// ---- 并发 ----

func TestConcurrentNextAndReport(t *testing.T) {
	pool, st, _ := setupPool(t, 5)

	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				uk, err := pool.Next()
				if err != nil {
					continue
				}
				pool.RecordUsage(uk.ID)
				// 混合成功与普通 4xx：都不该改变状态。
				if i%3 == 0 {
					pool.Report(uk.ID, Outcome{StatusCode: 200})
				}
			}
		}()
	}
	wg.Wait()

	if err := pool.Flush(); err != nil {
		t.Fatalf("Flush() 失败: %v", err)
	}
	keys, err := st.UpstreamKeys.List()
	if err != nil {
		t.Fatalf("List() 失败: %v", err)
	}
	var total int64
	for _, uk := range keys {
		total += uk.RequestCount
		if uk.State != store.StateAvailable {
			t.Errorf("并发后 Key %d state = %q, want available", uk.ID, uk.State)
		}
	}
	if total != goroutines*iterations {
		t.Errorf("总调用计数 = %d, want %d", total, goroutines*iterations)
	}
}

// ---- 重启保留（AC10 上半段）----

func TestRestartPreservesState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restart.db")

	db1, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	st1 := store.NewStore(db1)
	id, err := st1.UpstreamKeys.Create(&store.UpstreamKey{Name: "k", APIKey: "fc-restart-1"})
	if err != nil {
		t.Fatalf("创建 Key 失败: %v", err)
	}
	pool1, err := New(st1.UpstreamKeys, newFakeClock(time.Now()), Config{
		DefaultCooldown: 60 * time.Second, FlushInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() 失败: %v", err)
	}
	pool1.Report(id, Outcome{StatusCode: 402})
	db1.Close()

	// 模拟重启：同一文件重新打开。
	db2, err := store.Open(path)
	if err != nil {
		t.Fatalf("重启后打开库失败: %v", err)
	}
	defer db2.Close()
	st2 := store.NewStore(db2)
	pool2, err := New(st2.UpstreamKeys, newFakeClock(time.Now()), Config{
		DefaultCooldown: 60 * time.Second, FlushInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("重启后 New() 失败: %v", err)
	}

	uk := mustGet(t, st2, id)
	if uk.State != store.StateExhausted {
		t.Errorf("重启后 state = %q, want exhausted（终态必须保留）", uk.State)
	}
	if _, err := pool2.Next(); !errors.Is(err, ErrNoKeyAvailable) {
		t.Errorf("重启后 Next() = %v, want ErrNoKeyAvailable", err)
	}
}
