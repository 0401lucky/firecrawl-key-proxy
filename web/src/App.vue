<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, authState } from './api/client'
import { useTheme } from './composables/useTheme'
import { useToast } from './composables/useToast'

const route = useRoute()
const router = useRouter()
const { theme, toggle } = useTheme()
const { toasts, remove } = useToast()

const navItems = [
  { to: '/', label: '总览', index: '01' },
  { to: '/keys', label: '上游 Key', index: '02' },
  { to: '/api-keys', label: 'API Key', index: '03' },
]

async function logout() {
  try {
    await api.logout()
  } catch {
    /* 会话可能已失效，仍回登录页 */
  }
  authState.authenticated = false
  router.push('/login')
}

const ready = computed(() => authState.ready)
const isLogin = computed(() => route.path === '/login')
</script>

<template>
  <!-- 登录页不套仪表台骨架 -->
  <div v-if="!ready || isLogin" class="min-h-screen bg-instrument">
    <router-view v-if="!isLogin" />
    <router-view v-else />
  </div>

  <div v-else class="flex min-h-screen bg-instrument">
    <!-- 左侧仪表轨：两种主题下都是深色底，故文字色不跟随主题 -->
    <aside
      class="fixed inset-y-0 left-0 z-20 flex w-52 flex-col border-r border-ink-line bg-ink-raised"
    >
      <div class="flex items-center gap-2.5 px-5 pb-5 pt-6">
        <div class="grid h-8 w-8 place-items-center rounded bg-amber-400 font-mono text-sm font-bold text-slate-950 shadow-lamp">
          F
        </div>
        <div class="leading-tight">
          <div class="font-mono text-xs font-semibold tracking-[0.18em] text-slate-100">
            FIRECRAWL
          </div>
          <div class="mt-0.5 text-[11px] text-slate-400">多 Key 反向代理</div>
        </div>
      </div>

      <nav class="mt-1 flex-1 space-y-1 px-3">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="rail-link group"
          active-class="!bg-amber-400/15 !text-amber-200 font-medium"
        >
          <span class="rail-index group-hover:text-slate-300">{{ item.index }}</span>
          {{ item.label }}
        </RouterLink>
      </nav>

      <div class="space-y-1 border-t border-ink-line px-3 py-4">
        <button class="rail-link group w-full" @click="toggle">
          <span class="rail-index group-hover:text-slate-300">TT</span>
          {{ theme === 'dark' ? '浅色模式' : '深色模式' }}
        </button>
        <button class="rail-link group w-full" @click="logout">
          <span class="rail-index group-hover:text-slate-300">XX</span>
          登出
        </button>
      </div>
    </aside>

    <!-- 内容区 -->
    <main class="ml-52 flex-1 p-8">
      <router-view />
    </main>
  </div>

  <!-- Toast 容器 -->
  <div class="pointer-events-none fixed right-4 top-4 z-50 flex w-80 flex-col gap-2">
    <TransitionGroup name="toast">
      <div
        v-for="t in toasts"
        :key="t.id"
        class="pointer-events-auto flex items-start justify-between gap-3 rounded-md border px-4 py-3 text-sm shadow-lg animate-toastIn"
        :class="
          t.type === 'success'
            ? 'border-emerald-500/50 bg-white text-slate-800 dark:bg-ink-raised dark:text-slate-100'
            : 'border-red-500/50 bg-white text-slate-800 dark:bg-ink-raised dark:text-slate-100'
        "
      >
        <span class="flex items-center gap-2">
          <span
            class="mt-0.5 h-2 w-2 shrink-0 rounded-full"
            :class="t.type === 'success' ? 'bg-emerald-500' : 'bg-red-500'"
          />
          <span>{{ t.text }}</span>
        </span>
        <button class="text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-100" @click="remove(t.id)">
          ✕
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: all 0.25s ease;
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
