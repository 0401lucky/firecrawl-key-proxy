package store

import (
	"testing"
)

// setupStats 建一个带两个上游 Key 的库，返回 CallStats 仓储与 Key id。
func setupStats(t *testing.T) (*CallStatsRepo, int64, int64) {
	t.Helper()
	st := openTestDB(t)
	id1, err := st.UpstreamKeys.Create(&UpstreamKey{Name: "k1", APIKey: "fc-stats-1"})
	if err != nil {
		t.Fatalf("创建 Key 1 失败: %v", err)
	}
	id2, err := st.UpstreamKeys.Create(&UpstreamKey{Name: "k2", APIKey: "fc-stats-2"})
	if err != nil {
		t.Fatalf("创建 Key 2 失败: %v", err)
	}
	return st.CallStats, id1, id2
}

// Increment 必须幂等合并：同一桶重复刷盘是叠加而非替换（keypool Flush 每 10s 一次，
// 跨多个周期同一桶会反复到达）。
func TestCallStatsIncrementUpsert(t *testing.T) {
	repo, id1, _ := setupStats(t)
	hour := int64(1_700_000_000) // 小时起点由 keypool 对齐，仓储不校验

	if err := repo.Increment([]CallStat{{Hour: hour, UpstreamKeyID: id1, StatusClass: 1, Calls: 2}}); err != nil {
		t.Fatalf("第一次 Increment 失败: %v", err)
	}
	if err := repo.Increment([]CallStat{{Hour: hour, UpstreamKeyID: id1, StatusClass: 1, Calls: 3}}); err != nil {
		t.Fatalf("第二次 Increment 失败: %v", err)
	}

	rows, err := repo.QueryWindow(hour)
	if err != nil {
		t.Fatalf("QueryWindow 失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("桶数 = %d, want 1（同桶必须合并）", len(rows))
	}
	if rows[0].Calls != 5 {
		t.Errorf("calls = %d, want 5（叠加而非替换）", rows[0].Calls)
	}
}

// QueryWindow 按下界过滤：小时 < startHour 的桶不返回。
func TestCallStatsQueryWindowFilters(t *testing.T) {
	repo, id1, _ := setupStats(t)
	old, cur := int64(1_700_000_000), int64(1_700_003_600)
	if err := repo.Increment([]CallStat{
		{Hour: old, UpstreamKeyID: id1, StatusClass: 1, Calls: 9},
		{Hour: cur, UpstreamKeyID: id1, StatusClass: 1, Calls: 1},
	}); err != nil {
		t.Fatalf("Increment 失败: %v", err)
	}

	rows, err := repo.QueryWindow(cur)
	if err != nil {
		t.Fatalf("QueryWindow 失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Hour != cur {
		t.Fatalf("窗口过滤后 = %+v, want 仅 cur 小时", rows)
	}
}

// PerKey 按上游 Key 聚合，按次数降序。
func TestCallStatsPerKey(t *testing.T) {
	repo, id1, id2 := setupStats(t)
	hour := int64(1_700_000_000)
	if err := repo.Increment([]CallStat{
		{Hour: hour, UpstreamKeyID: id1, StatusClass: 1, Calls: 3},
		{Hour: hour, UpstreamKeyID: id2, StatusClass: 1, Calls: 7},
		{Hour: hour + 3600, UpstreamKeyID: id2, StatusClass: 3, Calls: 5},
	}); err != nil {
		t.Fatalf("Increment 失败: %v", err)
	}

	totals, err := repo.PerKey(hour)
	if err != nil {
		t.Fatalf("PerKey 失败: %v", err)
	}
	if len(totals) != 2 {
		t.Fatalf("Key 数 = %d, want 2", len(totals))
	}
	if totals[0].UpstreamKeyID != id2 || totals[0].Calls != 12 {
		t.Errorf("首位 = key %d calls %d, want key %d calls 12（降序）", totals[0].UpstreamKeyID, totals[0].Calls, id2)
	}
	if totals[1].UpstreamKeyID != id1 || totals[1].Calls != 3 {
		t.Errorf("次位 = key %d calls %d, want key %d calls 3", totals[1].UpstreamKeyID, totals[1].Calls, id1)
	}
}

// DeleteBefore 只删 hour < cutoff 的桶。
func TestCallStatsDeleteBefore(t *testing.T) {
	repo, id1, _ := setupStats(t)
	old, cur := int64(1_700_000_000), int64(1_700_003_600)
	if err := repo.Increment([]CallStat{
		{Hour: old, UpstreamKeyID: id1, StatusClass: 1, Calls: 1},
		{Hour: cur, UpstreamKeyID: id1, StatusClass: 1, Calls: 1},
	}); err != nil {
		t.Fatalf("Increment 失败: %v", err)
	}

	n, err := repo.DeleteBefore(cur)
	if err != nil {
		t.Fatalf("DeleteBefore 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("删除条数 = %d, want 1", n)
	}
	rows, err := repo.QueryWindow(old)
	if err != nil {
		t.Fatalf("QueryWindow 失败: %v", err)
	}
	if len(rows) != 1 || rows[0].Hour != cur {
		t.Errorf("清理后剩余 = %+v, want 仅 cur 小时", rows)
	}
}

// 删除上游 Key 后其统计桶必须级联清除（foreign_keys ON 生效）。
func TestCallStatsCascadeOnKeyDelete(t *testing.T) {
	st := openTestDB(t)
	id, err := st.UpstreamKeys.Create(&UpstreamKey{Name: "tmp", APIKey: "fc-stats-tmp"})
	if err != nil {
		t.Fatalf("创建 Key 失败: %v", err)
	}
	hour := int64(1_700_000_000)
	if err := st.CallStats.Increment([]CallStat{{Hour: hour, UpstreamKeyID: id, StatusClass: 1, Calls: 4}}); err != nil {
		t.Fatalf("Increment 失败: %v", err)
	}

	if err := st.UpstreamKeys.Delete(id); err != nil {
		t.Fatalf("删除 Key 失败: %v", err)
	}
	rows, err := st.CallStats.QueryWindow(hour)
	if err != nil {
		t.Fatalf("QueryWindow 失败: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("删 Key 后残留 %d 个桶, want 0（级联清除）", len(rows))
	}
}
