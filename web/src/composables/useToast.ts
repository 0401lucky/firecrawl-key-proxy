import { ref } from 'vue'

export interface ToastItem {
  id: number
  type: 'success' | 'error'
  text: string
}

const toasts = ref<ToastItem[]>([])
let seq = 0

/** 全局 Toast：写操作结果反馈。 */
export function useToast() {
  function push(type: ToastItem['type'], text: string) {
    const id = ++seq
    toasts.value.push({ id, type, text })
    window.setTimeout(() => remove(id), 3600)
  }
  function remove(id: number) {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }
  return { toasts, push, remove }
}
