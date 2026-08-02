package keypool

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"firecrawl-proxy/internal/store"
)

// 回归：Next/NextExcluding/GetByID 必须返回副本，不得交出指向池内部 entry 的指针。
//
// 曾经的缺陷：返回 &e.uk，而 internal/admin 的 PATCH 处理器拿到后在锁外直接写
// State/Enabled/CooldownUntil，与持锁的 Report/next/Snapshot 并发冲突——
// 管理员在有流量时点一下「禁用」就是一次真实的数据竞争。
func TestPoolReturnsCopiesNotInternalPointers(t *testing.T) {
	pool, _ := newTestPool(t)

	// GetByID 两次不应是同一指针。
	a, err := pool.GetByID(1)
	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	b, _ := pool.GetByID(1)
	if a == b {
		t.Fatal("GetByID 返回了指向池内部的指针（两次调用同址）")
	}

	// 改写返回值不得影响池的权威状态。
	a.State = store.StateExhausted
	a.Enabled = false
	a.Name = "被外部改掉了"

	keys, counts := pool.Snapshot()
	for _, ks := range keys {
		if ks.Key.ID != 1 {
			continue
		}
		if ks.Key.State != store.StateAvailable || !ks.Key.Enabled {
			t.Errorf("锁外修改污染了池状态: state=%s enabled=%v", ks.Key.State, ks.Key.Enabled)
		}
		if ks.Key.Name == "被外部改掉了" {
			t.Error("锁外修改污染了池中的 Name")
		}
	}
	if counts[store.StateExhausted] != 0 {
		t.Errorf("不应出现 exhausted 计数: %v", counts)
	}

	// Next 同样返回副本。
	n, err := pool.Next()
	if err != nil {
		t.Fatalf("Next 失败: %v", err)
	}
	inner, _ := pool.GetByID(n.ID)
	if n == inner {
		t.Error("Next() 返回了指向池内部的指针")
	}
	n.State = store.StateInvalid
	if again, _ := pool.GetByID(n.ID); again.State != store.StateAvailable {
		t.Errorf("改写 Next 返回值污染了池状态: %s", again.State)
	}
}

// 回归：模拟面板 PATCH 与代理转发并发进行——面板拿 GetByID 改字段，
// 转发侧持续 Next/Report/Snapshot。在 -race 下应无告警；
// 无 race 检测的环境下至少保证不 panic、状态自洽。
func TestConcurrentAdminPatchAndForwarding(t *testing.T) {
	pool, _ := newTestPool(t)

	var forwarders sync.WaitGroup
	stop := make(chan struct{})

	// 转发侧：不断选 Key、上报结果、取快照。
	for i := 0; i < 4; i++ {
		forwarders.Add(1)
		go func() {
			defer forwarders.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if k, err := pool.Next(); err == nil {
					_ = k.APIKey // 转发期间读 APIKey
					pool.Report(k.ID, Outcome{StatusCode: 200})
					pool.RecordUsage(k.ID)
				}
				pool.Snapshot()
			}
		}()
	}

	// 面板侧：模拟 handlePatchUpstreamKey 的用法，跑完固定轮次即收工。
	for i := 0; i < 200; i++ {
		uk, err := pool.GetByID(1)
		if err != nil {
			continue
		}
		uk.Enabled = i%2 == 0
		uk.State = store.StateAvailable
		uk.LastError = nil
		if err := pool.Reload(); err != nil {
			t.Fatalf("Reload 失败: %v", err)
		}
	}

	close(stop)
	forwarders.Wait()
}

func newTestPool(t *testing.T) (*Pool, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.NewStore(db)
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "A", APIKey: "fc-aaaa1111"})
	st.UpstreamKeys.Create(&store.UpstreamKey{Name: "B", APIKey: "fc-bbbb2222"})

	pool, err := New(st.UpstreamKeys, RealClock{}, Config{
		DefaultCooldown: time.Minute, FlushInterval: time.Minute,
	})
	if err != nil {
		t.Fatalf("构造池失败: %v", err)
	}
	return pool, st
}
