<script setup lang="ts">
import { computed } from 'vue'
import type { CallSeriesPoint } from '../api/types'

// 纯 SVG 柱状图：每根柱分「成功(cyan) / 非 2xx(rose)」两段，悬停显示详情。
// 无外部图表库依赖。宽度自适应（viewBox + preserveAspectRatio），高度固定。
const props = defineProps<{
  points: CallSeriesPoint[]
  granularity: 'hour' | 'day'
}>()

const W = 720
const H = 150
const PAD = { top: 6, right: 4, bottom: 22, left: 4 }

const maxCalls = computed(() => Math.max(1, ...props.points.map((p) => p.calls)))

interface Bar {
  x: number
  w: number
  yOk: number
  hOk: number
  hErr: number
  calls: number
  errors: number
  label: string
}

const bars = computed<Bar[]>(() => {
  const n = Math.max(1, props.points.length)
  const innerW = W - PAD.left - PAD.right
  const innerH = H - PAD.top - PAD.bottom
  const bw = innerW / n
  const gap = Math.min(2, bw * 0.3)
  return props.points.map((p, i) => {
    const ok = Math.max(0, p.calls - p.errors)
    const scale = (v: number) => (v / maxCalls.value) * innerH
    const hOk = Math.max(ok > 0 ? 1 : 0, scale(ok))
    const hErr = Math.max(p.errors > 0 ? 1 : 0, scale(p.errors))
    const x = PAD.left + i * bw + gap / 2
    const yOk = PAD.top + innerH - hOk
    const d = new Date(p.ts * 1000)
    const label =
      props.granularity === 'hour'
        ? `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}时`
        : `${d.getMonth() + 1}/${d.getDate()}`
    return {
      x,
      w: bw - gap,
      yOk,
      hOk,
      hErr,
      calls: p.calls,
      errors: p.errors,
      label,
    }
  })
})

// x 轴刻度：点少全标，点多取 5 个均匀位置（首/1/4/1/2/3/4/尾）。
const xTicks = computed(() => {
  const n = props.points.length
  const idxs = n <= 6 ? [...Array(n).keys()] : [0, Math.floor(n / 4), Math.floor(n / 2), Math.floor((3 * n) / 4), n - 1]
  const innerW = W - PAD.left - PAD.right
  return idxs.map((i) => {
    const p = props.points[i]
    const d = new Date(p.ts * 1000)
    const label =
      props.granularity === 'hour'
        ? `${String(d.getHours()).padStart(2, '0')}时`
        : `${d.getMonth() + 1}/${d.getDate()}`
    return { x: PAD.left + (i + 0.5) * (innerW / n), label }
  })
})

const empty = computed(() => bars.value.every((b) => b.calls === 0))
</script>

<template>
  <div class="relative w-full">
    <svg
      :viewBox="`0 0 ${W} ${H}`"
      class="h-36 w-full"
      preserveAspectRatio="none"
      role="img"
      aria-label="调用量趋势图"
    >
      <title>调用量趋势</title>
      <desc>按时间分桶的调用量，cyan 为成功请求，rose 为非 2xx 请求。</desc>

      <!-- 网格基线 -->
      <line
        :x1="PAD.left" :x2="W - PAD.right" :y1="H - PAD.bottom" :y2="H - PAD.bottom"
        class="stroke-slate-300 dark:stroke-ink-line/60"
        stroke-width="1"
      />

      <!-- 柱：先画错误段（叠在上方），再画成功段 -->
      <g v-for="b in bars" :key="b.x">
        <rect
          v-if="b.hErr > 0"
          :x="b.x" :width="b.w"
          :y="b.yOk - b.hErr" :height="b.hErr"
          class="fill-rose-500/85 dark:fill-rose-400/85"
          rx="1"
        />
        <rect
          v-if="b.hOk > 0"
          :x="b.x" :width="b.w"
          :y="b.yOk" :height="b.hOk"
          class="fill-cyan-600/85 dark:fill-cyan-400/85"
          rx="1"
        />
        <title>{{ b.label }}：共 {{ b.calls }} 次调用，其中 {{ b.errors }} 次非 2xx</title>
      </g>

      <!-- x 轴刻度 -->
      <text
        v-for="t in xTicks"
        :key="t.x"
        :x="t.x" :y="H - 6"
        text-anchor="middle"
        class="fill-slate-500 dark:fill-slate-400"
        font-size="10"
      >
        {{ t.label }}
      </text>
    </svg>

    <!-- 全零空态：图表区域居中提示 -->
    <div
      v-if="empty"
      class="pointer-events-none absolute inset-0 flex items-center justify-center text-xs t-muted"
    >
      该时段暂无调用数据
    </div>
  </div>
</template>
