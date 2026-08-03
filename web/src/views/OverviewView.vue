<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { api } from '../api/client'
import type { CallSeriesPoint, CallStats, Overview, StatsWindow, UpstreamKey } from '../api/types'
import { usePolling } from '../composables/usePolling'
import StateBadge from '../components/StateBadge.vue'
import CallTrendChart from '../components/CallTrendChart.vue'

const overview = ref<Overview | null>(null)
const keys = ref<UpstreamKey[]>([])
const stats = ref<CallStats | null>(null)
const win = ref<StatsWindow>('24h')
const windows: StatsWindow[] = ['24h', '7d', '30d']
const lastUpdated = ref(0)

async function load() {
  const [ov, ks, st] = await Promise.all([api.overview(), api.upstreamKeys(), api.stats(win.value)])
  overview.value = ov
  keys.value = ks
  stats.value = st
  lastUpdated.value = Date.now()
}

// 窗口切换立即重新拉取（轮询仍按 5s）。
watch(win, () => void load())

// 5 秒轮询；页面不可见时自动暂停（usePolling 内部处理）。
usePolling(load, 5000)

// 「N 秒前更新」倒计时
const updatedAgo = ref('')
let agoTimer: number | undefined
function tickAgo() {
  const s = Math.max(0, Math.round((Date.now() - lastUpdated.value) / 1000))
  updatedAgo.value = s <= 1 ? '刚刚更新' : `${s} 秒前更新`
}
tickAgo()
agoTimer = window.setInterval(tickAgo, 1000)
onUnmounted(() => window.clearInterval(agoTimer))

const poolPercent = computed(() => {
  if (!overview.value || overview.value.credits_total_sum <= 0) return 0
  return Math.min(100, Math.round((overview.value.credits_remaining_sum / overview.value.credits_total_sum) * 100))
})

const counts = computed(() => overview.value?.key_counts ?? {})
const countList = computed(() => {
  const order: Array<[string, string]> = [
    ['available', '可用'],
    ['cooling', '冷却'],
    ['exhausted', '耗尽'],
    ['invalid', '失效'],
    ['disabled', '禁用'],
  ]
  return order.map(([k, label]) => ({ label, n: counts.value[k] ?? 0 }))
})

// 冷却倒计时：以后端快照秒数本地递减，每次轮询用服务端值校正。
const now = ref(Date.now())
let cdTimer: number | undefined
cdTimer = window.setInterval(() => (now.value = Date.now()), 1000)
onUnmounted(() => window.clearInterval(cdTimer))

function cooldownText(k: UpstreamKey): string {
  if (k.state !== 'cooling') return ''
  const secs = Math.max(0, k.cooldown_remaining - Math.floor((Date.now() - lastUpdated.value) / 1000))
  if (secs <= 0) return '即将恢复'
  const m = Math.floor(secs / 60)
  const s = secs % 60
  return m > 0 ? `${m}分${s}秒` : `${s}秒`
}

// ---- 调用数据面板 ----

// 本地日键：浏览器时区的日期，避免容器 UTC 偏差导致「今日」错位 8 小时。
function localDayKey(ts: number): string {
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

// 今日调用：API 永远按小时返回，把本地今天 0 点起的桶求和。
const todayCalls = computed(() => {
  if (!stats.value) return 0
  const today = localDayKey(Date.now())
  return stats.value.series
    .filter((p) => localDayKey(p.ts * 1000) === today)
    .reduce((s, p) => s + p.calls, 0)
})

// 近 24 小时调用：小时 series 最后 24 个桶。
const last24hCalls = computed(() => {
  if (!stats.value) return 0
  return stats.value.series.slice(-24).reduce((s, p) => s + p.calls, 0)
})

// 趋势图数据：24h 逐小时；7d/30d 按本地日聚合（API 返回的始终是小时桶）。
const chartSeries = computed<CallSeriesPoint[]>(() => {
  if (!stats.value) return []
  if (win.value === '24h') return stats.value.series
  const byDay = new Map<string, CallSeriesPoint>()
  for (const p of stats.value.series) {
    const key = localDayKey(p.ts * 1000)
    const cur = byDay.get(key)
    if (cur) {
      cur.calls += p.calls
      cur.errors += p.errors
    } else {
      byDay.set(key, { ts: p.ts, calls: p.calls, errors: p.errors })
    }
  }
  return [...byDay.values()]
})

const chartGranularity = computed<'hour' | 'day'>(() => (win.value === '24h' ? 'hour' : 'day'))

// 按上游 Key 分布：join 已轮询的上游 Key 列表（name/masked 复用，不重复拉取）。
const perKeyList = computed(() => {
  if (!stats.value) return []
  return stats.value.per_key.map((pk) => {
    const uk = keys.value.find((k) => k.id === pk.key_id)
    return {
      ...pk,
      name: uk?.name ?? `#${pk.key_id}`,
      masked: uk?.masked ?? '',
      pct: Math.round(pk.share * 100),
    }
  })
})

const successRateCls = computed(() => {
  const r = stats.value?.success_rate ?? 0
  if (r >= 0.95) return 'text-emerald-700 dark:text-emerald-400'
  if (r >= 0.7) return 'text-amber-700 dark:text-amber-400'
  return 'text-rose-700 dark:text-rose-400'
})
</script>

<template>
  <div class="max-w-5xl animate-rise">
    <header class="flex items-baseline justify-between">
      <h1 class="font-mono text-xl font-semibold tracking-wide t-primary">总览</h1>
      <span class="num text-xs t-muted">{{ updatedAgo }}</span>
    </header>

    <!-- 额度池 -->
    <section v-if="overview" class="surface mt-6 p-6">
      <div class="flex items-center justify-between">
        <div class="t-label">额度池</div>
        <div class="num text-sm t-secondary">
          <span class="text-lg font-semibold text-amber-600 dark:text-amber-400">
            {{ overview.credits_remaining_sum }}
          </span>
          <span class="mx-1">/</span>
          {{ overview.credits_total_sum }}
        </div>
      </div>
      <div class="mt-3 h-2.5 overflow-hidden rounded-full bg-slate-200 dark:bg-ink-line/60">
        <div
          class="h-full rounded-full bg-gradient-to-r from-amber-500 to-amber-300 transition-all duration-700"
          :style="{ width: poolPercent + '%' }"
        />
      </div>
      <div class="mt-2 flex justify-between text-xs t-muted">
        <span>{{ poolPercent }}% 可用</span>
        <span class="num">剩余 / 总量</span>
      </div>
    </section>

    <!-- 状态计数条 -->
    <div class="mt-4 flex flex-wrap gap-2">
      <div v-for="c in countList" :key="c.label" class="surface px-3 py-1.5 text-xs">
        <span class="t-muted">{{ c.label }}</span>
        <span class="num ml-1.5 font-semibold t-primary">{{ c.n }}</span>
      </div>
      <div class="surface px-3 py-1.5 text-xs">
        <span class="t-muted">API Key</span>
        <span class="num ml-1.5 font-semibold t-primary">{{ overview?.proxy_key_count ?? 0 }}</span>
      </div>
    </div>

    <!-- 调用数据 -->
    <section v-if="stats" class="surface mt-6 p-6">
      <div class="flex items-center justify-between">
        <div class="t-label">调用数据</div>
        <div class="flex gap-1 text-xs">
          <button
            v-for="w in windows"
            :key="w"
            type="button"
            class="num rounded-md px-2 py-1 transition-colors"
            :class="win === w ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400' : 't-muted hover:bg-ink-line/20'"
            @click="win = w"
          >
            {{ w }}
          </button>
        </div>
      </div>

      <!-- 汇总卡 -->
      <div class="mt-4 grid grid-cols-2 gap-3 md:grid-cols-4">
        <div class="rounded-lg border hairline p-3">
          <div class="t-label">窗口总调用</div>
          <div class="num mt-1 text-xl font-semibold t-primary">{{ stats.total_calls.toLocaleString() }}</div>
        </div>
        <div class="rounded-lg border hairline p-3">
          <div class="t-label">今日调用</div>
          <div class="num mt-1 text-xl font-semibold t-primary">{{ todayCalls.toLocaleString() }}</div>
        </div>
        <div class="rounded-lg border hairline p-3">
          <div class="t-label">近 24 小时</div>
          <div class="num mt-1 text-xl font-semibold t-primary">{{ last24hCalls.toLocaleString() }}</div>
        </div>
        <div class="rounded-lg border hairline p-3">
          <div class="t-label">成功率</div>
          <div class="num mt-1 text-xl font-semibold" :class="successRateCls">{{ (stats.success_rate * 100).toFixed(1) }}%</div>
        </div>
      </div>

      <!-- 趋势图 -->
      <div class="mt-5 flex items-center gap-4 text-[11px] t-muted">
        <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-cyan-600 dark:bg-cyan-400" />成功（2xx）</span>
        <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-rose-500 dark:bg-rose-400" />非 2xx</span>
        <span class="num ml-auto">{{ chartGranularity === 'hour' ? '逐小时' : '按日' }}</span>
      </div>
      <CallTrendChart :points="chartSeries" :granularity="chartGranularity" class="mt-2" />

      <!-- 按上游 Key 分布 -->
      <div v-if="perKeyList.length" class="mt-5">
        <div class="t-label">按上游 Key 分布</div>
        <div class="mt-2 space-y-2">
          <div v-for="pk in perKeyList" :key="pk.key_id" class="flex items-center gap-3">
            <div class="num w-12 shrink-0 text-right text-xs t-muted">{{ pk.pct }}%</div>
            <div class="h-3 min-w-0 flex-1 overflow-hidden rounded-full bg-slate-200 dark:bg-ink-line/60">
              <div
                class="h-full rounded-full bg-gradient-to-r from-cyan-600 to-cyan-400 dark:from-cyan-500 dark:to-cyan-300"
                :style="{ width: pk.pct + '%' }"
              />
            </div>
            <div class="w-40 shrink-0 truncate text-xs t-secondary" :title="pk.name">{{ pk.name }}</div>
            <div class="num w-16 shrink-0 text-right text-xs t-muted">{{ pk.calls.toLocaleString() }}</div>
          </div>
        </div>
      </div>
    </section>

    <!-- 上游 Key 卡片 -->
    <h2 class="t-label mt-8">上游 KEY 状态</h2>
    <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="(k, i) in keys"
        :key="k.id"
        class="surface p-4 animate-rise"
        :style="{ animationDelay: `${i * 40}ms` }"
      >
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <div class="truncate text-sm font-medium t-primary">{{ k.name }}</div>
            <div class="num mt-0.5 truncate text-[11px] t-muted">{{ k.masked }}</div>
          </div>
          <StateBadge :state="k.state" :disabled="!k.enabled" />
        </div>

        <div class="mt-3 flex items-end justify-between border-t hairline pt-3">
          <div>
            <div class="t-label">剩余额度</div>
            <div class="num mt-0.5 text-sm t-body">{{ k.credits_remaining ?? '—' }}</div>
          </div>
          <div class="text-right">
            <div v-if="k.state === 'cooling'" class="num text-xs text-amber-600 dark:text-amber-400">
              ⏳ {{ cooldownText(k) }}
            </div>
            <div v-else class="num text-xs t-muted">调用 {{ k.request_count }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- 空态：给一条明确的下一步，而不只是一句灰字 -->
    <div v-if="keys.length === 0" class="surface mt-3 px-6 py-10 text-center">
      <p class="text-sm t-secondary">还没有上游 Key。</p>
      <RouterLink to="/keys" class="btn-primary mt-4 inline-block">去录入第一个 Key</RouterLink>
    </div>
  </div>
</template>
