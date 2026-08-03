// Package keypool 实现上游 Key 的运行时池：加载、轮询选择、状态转移、冷却恢复。
//
// 内存是运行时权威，DB 是持久化副本。状态变更（冷却/耗尽/失效）同步写 DB；
// 调用计数在内存累加，按固定间隔批量刷盘。冷却恢复采用惰性判定——
// 在 Next() 里检查 cooldown_until 是否已过，不起后台定时任务。
package keypool

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"firecrawl-proxy/internal/store"
)

// ErrNoKeyAvailable 表示当前没有任何可用的上游 Key。
var ErrNoKeyAvailable = errors.New("没有可用的上游 Key")

// Config 是 Key 池的运行参数。
type Config struct {
	DefaultCooldown time.Duration // 429 无 Retry-After 头时的默认冷却时长
	FlushInterval   time.Duration // 用量计数批量刷盘间隔
	StatsRepo       *store.CallStatsRepo // 调用统计桶仓储；nil 时统计只累积不落库（测试可省略）
}

// statKey 是内存中调用统计桶的唯一键：小时 × 上游 Key × 状态类别。
// 用结构体而非字符串拼接，避免解析开销与格式漂移。
type statKey struct {
	hour    int64
	keyID   int64
	class   int
}

// entry 包裹 store.UpstreamKey，附加内存态字段。
type entry struct {
	uk store.UpstreamKey
}

// Pool 是上游 Key 的运行时池。所有公开方法并发安全。
//
// 为什么单个互斥锁就够：Key 数量是几十级别，Next() 是一次 O(n) 线性扫描
// 且本身要推进 cursor（写操作），读写锁与分片原子计数都拿不到好处，
// 反而让状态转移的一致性变复杂。
type Pool struct {
	mu     sync.Mutex
	keys   []*entry // 按 id 升序，保证轮询顺序稳定
	cursor int
	usage  map[int64]int64     // 待刷盘的调用增量（keyID → 次数）
	stats  map[statKey]int64   // 待刷盘的统计桶增量（hour×key×class → 次数）

	repo      *store.UpstreamKeyRepo
	statsRepo *store.CallStatsRepo
	clock     Clock
	cfg       Config
}

// New 从 DB 加载全部上游 Key 构造池。
func New(repo *store.UpstreamKeyRepo, clock Clock, cfg Config) (*Pool, error) {
	p := &Pool{
		repo:      repo,
		statsRepo: cfg.StatsRepo,
		clock:     clock,
		cfg:       cfg,
		usage:     make(map[int64]int64),
		stats:     make(map[statKey]int64),
	}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Run 启动用量刷盘循环，直到 ctx 取消。由 main 以 goroutine 方式调用。
func (p *Pool) Run(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.Flush(); err != nil {
				slog.Error("用量刷盘失败", "error", err.Error())
			}
		case <-ctx.Done():
			return
		}
	}
}

// Next 返回下一个可用 Key（轮询）。
func (p *Pool) Next() (*store.UpstreamKey, error) {
	return p.next(nil)
}

// NextExcluding 返回下一个可用 Key，排除本次请求已尝试过的。
// 供故障转移时避免重复撞同一个 Key。
func (p *Pool) NextExcluding(tried []int64) (*store.UpstreamKey, error) {
	return p.next(tried)
}

// next 是选择算法的核心：单次线性扫描，跳过 disabled/exhausted/invalid，
// 对 cooling 做惰性恢复（cooldown_until 已过则就地转回 available），
// 在候选集合上按游标轮询。
func (p *Pool) next(exclude []int64) (*store.UpstreamKey, error) {
	excluded := make(map[int64]struct{}, len(exclude))
	for _, id := range exclude {
		excluded[id] = struct{}{}
	}

	p.mu.Lock()
	now := p.clock.Now()
	var (
		cands           []*entry
		pendingRecovery []store.UpstreamKey // 惰性恢复的副本，锁外落库
	)
	for _, e := range p.keys {
		uk := &e.uk
		if !uk.Enabled {
			continue
		}
		switch uk.State {
		case store.StateAvailable:
			// 可直接选中。
		case store.StateCooling:
			if uk.CooldownUntil == nil || now.Before(*uk.CooldownUntil) {
				continue // 仍在冷却中
			}
			// 冷却到期：惰性恢复为 available。
			uk.State = store.StateAvailable
			uk.CooldownUntil = nil
			pendingRecovery = append(pendingRecovery, *uk)
		default: // exhausted / invalid
			continue
		}
		if _, skip := excluded[uk.ID]; !skip {
			cands = append(cands, e)
		}
	}
	if len(cands) == 0 {
		p.mu.Unlock()
		return nil, ErrNoKeyAvailable
	}
	idx := p.cursor % len(cands)
	p.cursor++
	// 返回副本而非 &chosen.uk：调用方会在锁外长时间持有它（转发期间读 APIKey），
	// 而 Report/SetCredits/面板 PATCH 会并发改同一结构体。交出内部指针等于
	// 把池的权威状态暴露到锁外，构成数据竞争。
	chosen := cands[idx].uk
	p.mu.Unlock()

	// 锁外落库惰性恢复的状态变更（不阻塞选择路径）。
	for i := range pendingRecovery {
		if err := p.repo.Update(&pendingRecovery[i]); err != nil {
			slog.Error("冷却恢复写库失败", "key_id", pendingRecovery[i].ID, "error", err.Error())
		}
	}
	return &chosen, nil
}

// Report 依据一次上游请求的结果更新该 Key 的状态，并同步写 DB。
// 网络层错误与 408/5xx 不改变状态（见 classify）。按 keyID 而非指针查找，
// 目标已被删除时静默丢弃。
func (p *Pool) Report(keyID int64, o Outcome) {
	tr := classify(o, p.cfg.DefaultCooldown)

	p.mu.Lock()
	var e *entry
	for _, c := range p.keys {
		if c.uk.ID == keyID {
			e = c
			break
		}
	}
	if e == nil || tr.state == "" {
		p.mu.Unlock()
		return
	}
	uk := &e.uk
	uk.State = tr.state
	if tr.state == store.StateCooling {
		until := p.clock.Now().Add(tr.cooldown)
		uk.CooldownUntil = &until
	} else {
		uk.CooldownUntil = nil
	}
	if tr.errMsg != "" {
		msg := tr.errMsg
		uk.LastError = &msg
	}
	snapshot := *uk // 复制出锁，锁外落库
	p.mu.Unlock()

	if err := p.repo.Update(&snapshot); err != nil {
		slog.Error("状态转移写库失败",
			"key_id", keyID, "state", string(tr.state), "error", err.Error())
	}
}

// RecordUsage 累加一次调用计数，由 C3 在请求完成后调用。
// 必须同时做两件事：
//  1. 累加待刷盘 map（持久化路径：Flush 按固定间隔写 DB）；
//  2. 累加内存条目自身的 RequestCount（展示路径：面板读 Snapshot）。
// 只做 1 会让面板的「调用数」停在最近一次 Reload 时的值——Flush 只写 DB、
// 不搬回内存，而 Reload 仅在面板增删改 Key 时触发（历史 bug）。
func (p *Pool) RecordUsage(keyID int64) {
	p.mu.Lock()
	p.usage[keyID]++
	for _, e := range p.keys {
		if e.uk.ID == keyID {
			e.uk.RequestCount++
			break
		}
	}
	p.mu.Unlock()
}

// RecordCall 累加一次调用统计，由 C3 在请求完成后调用，调用点与 RecordUsage 同位。
// 按当前时间的小时桶 × 状态类别聚合到内存，Flush 时批量 upsert 到 call_stats_buckets；
// 不增加每请求一次 DB 写。网络错误不调本方法（与 request_count 口径一致）。
func (p *Pool) RecordCall(keyID int64, statusCode int) {
	p.mu.Lock()
	hour := p.clock.Now().Truncate(time.Hour).Unix()
	k := statKey{hour: hour, keyID: keyID, class: statusClass(statusCode)}
	p.stats[k]++
	p.mu.Unlock()
}

// statusClass 把 HTTP 状态码映射为聚合类别（见 store.StatusClass*）。
func statusClass(code int) int {
	switch {
	case code >= 200 && code < 300:
		return store.StatusClass2xx
	case code >= 300 && code < 400:
		return store.StatusClass3xx
	case code >= 400 && code < 500:
		return store.StatusClass4xx
	default:
		return store.StatusClass5xx
	}
}

// Flush 把内存中累计的调用增量与统计桶增量批量写入 DB 后清空。
// 先在锁内取走两个 map，再在锁外执行 DB 写——不持锁做 I/O。
// 任一失败返回聚合错误（调用方记日志），已取走的增量按既有语义丢弃。
func (p *Pool) Flush() error {
	p.mu.Lock()
	var usage map[int64]int64
	var stats map[statKey]int64
	if len(p.usage) > 0 {
		usage = p.usage
		p.usage = make(map[int64]int64)
	}
	if len(p.stats) > 0 {
		stats = p.stats
		p.stats = make(map[statKey]int64)
	}
	p.mu.Unlock()

	var errs []error
	if len(usage) > 0 {
		if err := p.repo.IncrementUsage(usage); err != nil {
			errs = append(errs, err)
		}
	}
	if len(stats) > 0 && p.statsRepo != nil {
		rows := make([]store.CallStat, 0, len(stats))
		for k, n := range stats {
			rows = append(rows, store.CallStat{
				Hour: k.hour, UpstreamKeyID: k.keyID, StatusClass: k.class, Calls: n,
			})
		}
		if err := p.statsRepo.Increment(rows); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetByID 按 id 返回内存中上游 Key 的副本（运行时权威的快照）。
// 返回副本而非内部指针：调用方（面板 PATCH、job 粘连）会在锁外读写它，
// 交出内部指针会让这些修改绕过锁直接改动池状态。
func (p *Pool) GetByID(keyID int64) (*store.UpstreamKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.keys {
		if e.uk.ID == keyID {
			cp := e.uk
			return &cp, nil
		}
	}
	return nil, ErrNoKeyAvailable
}

// Reload 从 DB 重新加载全部 Key。供面板增删改 Key 后调用，变更立即生效。
// 替换前先把当前 usage 刷盘，避免丢失未落库的计数。
func (p *Pool) Reload() error {
	list, err := p.repo.List()
	if err != nil {
		return err
	}
	if err := p.Flush(); err != nil {
		slog.Error("Reload 前刷盘失败", "error", err.Error())
	}
	entries := make([]*entry, 0, len(list))
	for i := range list {
		entries = append(entries, &entry{uk: list[i]})
	}
	p.mu.Lock()
	p.keys = entries
	p.cursor = 0
	p.mu.Unlock()
	return nil
}

// KeySnapshot 是单个 Key 的运行时状态快照。APIKey 不得被直接序列化或打日志。
type KeySnapshot struct {
	Key               store.UpstreamKey
	CooldownRemaining int64 // 冷却剩余秒数，仅 cooling 时 > 0
}

// Snapshot 返回全部 Key 的状态快照与按状态分类的计数。
// 供面板展示与 C3 构造 503 错误体使用。
func (p *Pool) Snapshot() ([]KeySnapshot, map[store.KeyState]int) {
	p.mu.Lock()

	now := p.clock.Now()
	keys := make([]KeySnapshot, 0, len(p.keys))
	counts := make(map[store.KeyState]int)
	var pendingRecovery []store.UpstreamKey // 惰性恢复的副本，锁外落库
	for _, e := range p.keys {
		uk := &e.uk
		ks := KeySnapshot{Key: *uk}
		if uk.State == store.StateCooling && uk.CooldownUntil != nil {
			if now.After(*uk.CooldownUntil) {
				// 冷却到期：惰性恢复为 available（同 next()），
				// 让面板下一次轮询即看到真实状态，而非等请求触发。
				uk.State = store.StateAvailable
				uk.CooldownUntil = nil
				pendingRecovery = append(pendingRecovery, *uk)
				ks.Key = *uk
			} else {
				ks.CooldownRemaining = int64(uk.CooldownUntil.Sub(now).Seconds())
			}
		}
		keys = append(keys, ks)
		counts[uk.State]++
	}
	p.mu.Unlock()

	// 锁外落库惰性恢复的状态变更（不阻塞快照路径）。
	for i := range pendingRecovery {
		if err := p.repo.Update(&pendingRecovery[i]); err != nil {
			slog.Error("冷却恢复写库失败", "key_id", pendingRecovery[i].ID, "error", err.Error())
		}
	}
	return keys, counts
}

// SetCredits 写入该 Key 的额度数据（面板额度刷新结果）。
// 仅用于展示，不参与 Next() 的候选判断。
func (p *Pool) SetCredits(keyID int64, total, remaining int64) {
	p.mu.Lock()
	var e *entry
	for _, c := range p.keys {
		if c.uk.ID == keyID {
			e = c
			break
		}
	}
	if e == nil {
		p.mu.Unlock()
		return
	}
	t, r := total, remaining
	now := p.clock.Now()
	e.uk.CreditsTotal = &t
	e.uk.CreditsRemaining = &r
	e.uk.CreditsSyncedAt = &now
	snapshot := e.uk
	p.mu.Unlock()

	if err := p.repo.Update(&snapshot); err != nil {
		slog.Error("额度写库失败", "key_id", keyID, "error", err.Error())
	}
}
