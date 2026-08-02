<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { api } from '../api/client'
import type { Overview, UpstreamKey } from '../api/types'
import { usePolling } from '../composables/usePolling'
import StateBadge from '../components/StateBadge.vue'

const overview = ref<Overview | null>(null)
const keys = ref<UpstreamKey[]>([])
const lastUpdated = ref(0)

async function load() {
  const [ov, ks] = await Promise.all([api.overview(), api.upstreamKeys()])
  overview.value = ov
  keys.value = ks
  lastUpdated.value = Date.now()
}

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
