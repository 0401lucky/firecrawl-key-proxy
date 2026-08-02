import { ref, watchEffect } from 'vue'

const STORAGE_KEY = 'fc-proxy-theme'
export type Theme = 'light' | 'dark'

function initialTheme(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** 主题：跟随系统偏好，可手动切换并持久化到 localStorage。 */
export function useTheme() {
  const theme = ref<Theme>(initialTheme())

  watchEffect(() => {
    document.documentElement.classList.toggle('dark', theme.value === 'dark')
    localStorage.setItem(STORAGE_KEY, theme.value)
  })

  const toggle = () => {
    theme.value = theme.value === 'dark' ? 'light' : 'dark'
  }

  return { theme, toggle }
}
