import { onMounted, onUnmounted } from 'vue'

/**
 * 可见性感知的轮询：
 * - document.hidden 时暂停（后台标签页不浪费请求）；
 * - 切回页面时立即拉一次（避免看到陈旧数据），再按间隔恢复；
 * - 组件卸载时清理定时器；
 * - 上一次请求未返回时跳过本次 tick（防堆积）。
 */
export function usePolling(fn: () => Promise<unknown> | unknown, intervalMs: number) {
  let timer: number | undefined
  let inflight = false

  const tick = async () => {
    if (inflight) return
    inflight = true
    try {
      await fn()
    } catch {
      /* 错误由调用方自行展示 Toast 等；轮询不因单次失败中断 */
    } finally {
      inflight = false
    }
  }

  const start = () => {
    stop()
    if (document.hidden) return
    void tick() // 启动先立即拉一次
    timer = window.setInterval(() => {
      if (!document.hidden) void tick()
    }, intervalMs)
  }

  const stop = () => {
    if (timer !== undefined) window.clearInterval(timer)
    timer = undefined
  }

  const onVisibility = () => {
    if (document.hidden) {
      stop()
    } else {
      void tick()
      start()
    }
  }

  onMounted(() => {
    document.addEventListener('visibilitychange', onVisibility)
    start()
  })
  onUnmounted(() => {
    document.removeEventListener('visibilitychange', onVisibility)
    stop()
  })
}
