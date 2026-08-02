<script setup lang="ts">
const props = defineProps<{
  open: boolean
  title: string
  message: string
  confirmText?: string
  danger?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()
</script>

<template>
  <Teleport to="body">
    <Transition name="dlg">
      <div
        v-if="open"
        class="fixed inset-0 z-40 flex items-center justify-center bg-black/50 p-4 backdrop-blur-sm"
        @click.self="emit('cancel')"
      >
        <div
          class="w-full max-w-md rounded-lg border border-ink-line bg-white p-6 shadow-panel dark:bg-ink-raised"
          role="dialog"
          aria-modal="true"
        >
          <h3 class="font-mono text-sm font-semibold tracking-wide text-slate-800 dark:text-slate-100">
            {{ props.title }}
          </h3>
          <p class="mt-3 text-sm leading-relaxed text-slate-500 dark:text-slate-400">
            {{ props.message }}
          </p>
          <div class="mt-6 flex justify-end gap-3">
            <button
              class="rounded-md border border-ink-line px-4 py-2 text-sm text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-ink-line/60"
              @click="emit('cancel')"
            >
              取消
            </button>
            <button
              class="rounded-md px-4 py-2 text-sm font-medium text-white transition-colors"
              :class="
                props.danger
                  ? 'bg-red-600 hover:bg-red-500'
                  : 'bg-amber-500 text-black hover:bg-amber-400'
              "
              @click="emit('confirm')"
            >
              {{ props.confirmText ?? '确认' }}
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
