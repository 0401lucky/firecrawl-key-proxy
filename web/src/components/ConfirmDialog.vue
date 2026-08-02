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
        <div class="surface w-full max-w-md p-6" role="dialog" aria-modal="true">
          <h3 class="font-mono text-sm font-semibold tracking-wide t-primary">
            {{ props.title }}
          </h3>
          <p class="mt-3 text-sm leading-relaxed t-secondary">
            {{ props.message }}
          </p>
          <div class="mt-6 flex justify-end gap-3">
            <button class="btn-ghost py-2" @click="emit('cancel')">取消</button>
            <button
              class="rounded-md px-4 py-2 text-sm font-semibold transition-colors"
              :class="
                props.danger
                  ? 'bg-red-600 text-white hover:bg-red-500'
                  : 'bg-amber-500 text-slate-950 hover:bg-amber-400'
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
