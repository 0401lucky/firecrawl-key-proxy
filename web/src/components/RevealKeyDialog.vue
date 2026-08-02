<script setup lang="ts">
import { ref } from 'vue'
import { useToast } from '../composables/useToast'

// 明文只作为一次性 prop 传入；关闭即销毁组件、清空引用。
// 不写 store、不进 URL、不落 localStorage。
const props = defineProps<{
  open: boolean
  name: string
  plaintext: string
}>()

const emit = defineEmits<{ close: [] }>()
const { push } = useToast()
const copied = ref(false)

async function copy() {
  try {
    await navigator.clipboard.writeText(props.plaintext)
    copied.value = true
    push('success', '已复制到剪贴板')
    window.setTimeout(() => (copied.value = false), 2000)
  } catch {
    // 非 HTTPS 下 clipboard API 不可用：降级为提示手动选中复制。
    push('error', '浏览器未授予剪贴板权限，请手动选中复制')
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dlg">
      <div
        v-if="open"
        class="fixed inset-0 z-40 flex items-center justify-center bg-black/50 p-4 backdrop-blur-sm"
        @click.self="emit('close')"
      >
        <div
          class="w-full max-w-lg rounded-lg border border-amber-400/40 bg-white p-6 shadow-panel dark:bg-ink-raised"
          role="dialog"
          aria-modal="true"
        >
          <div class="flex items-start justify-between">
            <div>
              <h3 class="font-mono text-sm font-semibold tracking-wide text-amber-600 dark:text-amber-400">
                新的 API Key 已创建
              </h3>
              <p class="mt-1 text-xs text-slate-400 dark:text-slate-500">名称：{{ props.name }}</p>
            </div>
            <span class="rounded bg-amber-400/15 px-2 py-1 text-[11px] font-medium text-amber-600 dark:text-amber-400">
              仅此一次
            </span>
          </div>

          <div class="mt-4 rounded-md border border-ink-line bg-slate-50 p-3 dark:bg-ink">
            <code class="num block select-all break-all text-sm text-slate-800 dark:text-amber-200">
              {{ props.plaintext }}
            </code>
          </div>

          <p class="mt-3 text-xs leading-relaxed text-red-500 dark:text-red-400">
            关闭后无法再次查看，请立即保存到安全位置。数据库只存哈希，遗失只能重新创建。
          </p>

          <div class="mt-5 flex justify-end gap-3">
            <button
              class="rounded-md border border-ink-line px-4 py-2 text-sm text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-ink-line/60"
              @click="emit('close')"
            >
              关闭
            </button>
            <button
              class="rounded-md bg-amber-500 px-4 py-2 text-sm font-medium text-black transition-colors hover:bg-amber-400"
              @click="copy"
            >
              {{ copied ? '已复制 ✓' : '复制到剪贴板' }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.dlg-enter-active,
.dlg-leave-active {
  transition: opacity 0.2s ease;
}
.dlg-enter-active > div,
.dlg-leave-active > div {
  transition: transform 0.2s ease;
}
.dlg-enter-from,
.dlg-leave-to {
  opacity: 0;
}
.dlg-enter-from > div,
.dlg-leave-to > div {
  transform: scale(0.97) translateY(4px);
}
</style>
