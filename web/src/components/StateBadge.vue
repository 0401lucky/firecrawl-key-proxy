<script setup lang="ts">
import { computed } from 'vue'
import type { KeyState } from '../api/types'

const props = defineProps<{
  state: KeyState
  disabled?: boolean
}>()

// 状态灯语义：可用=绿、冷却=琥珀、额度耗尽=灰、已失效=红、已禁用=灰
// 浅色模式下一律用 600 档以上，400 档在白底只有约 2.8:1，读不清。
const meta = computed(() => {
  if (props.disabled) return { label: '已禁用', cls: 'text-slate-500 dark:text-slate-400', dot: 'bg-slate-400' }
  switch (props.state) {
    case 'available':
      return { label: '可用', cls: 'text-emerald-700 dark:text-emerald-400', dot: 'bg-emerald-500' }
    case 'cooling':
      return { label: '冷却中', cls: 'text-amber-700 dark:text-amber-400', dot: 'bg-amber-400' }
    case 'exhausted':
      return { label: '额度耗尽', cls: 'text-slate-600 dark:text-slate-300', dot: 'bg-slate-400' }
    case 'invalid':
      return { label: '已失效', cls: 'text-red-700 dark:text-red-400', dot: 'bg-red-500' }
    default:
      return { label: props.state, cls: 'text-slate-600 dark:text-slate-300', dot: 'bg-slate-400' }
  }
})
</script>

<template>
  <span class="inline-flex items-center gap-1.5 text-xs font-medium" :class="meta.cls">
    <span class="h-1.5 w-1.5 rounded-full" :class="meta.dot" />
    {{ meta.label }}
  </span>
</template>
