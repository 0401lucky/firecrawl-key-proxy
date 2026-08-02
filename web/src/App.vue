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
    <!-- 左侧仪表轨 -->
    <aside
      class="fixed inset-y-0 left-0 z-20 flex w-52 flex-col border-r border-ink-line bg-ink-raised/80 backdrop-blur dark:border-ink-line dark:bg-ink-raised/80"
    >
      <div class="flex items-center gap-2 px-5 pb-4 pt-6">
        <div class="grid h-8 w-8 place-items-center rounded bg-amber-400 font-mono text-sm font-bold text-black shadow-lamp">
          F
        </div>
        <div class="leading-tight">
          <div class="font-mono text-xs font-semibold tracking-widest text-slate-300 dark:text-slate-200">
            FIRECRAWL
          </div>
          <div class="text-[11px] text-slate-400 dark:text-slate-500">多 Key 反向代理</div>
        </div>
      </div>

      <nav class="mt-2 flex-1 space-y-1 px-3">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="group flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-600 transition-colors hover:bg-ink-line/40 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-ink-line/60 dark:hover:text-slate-100"
          active-class="!bg-amber-400/10 !text-amber-600 dark:!text-amber-300 font-medium"
        >
          <span class="font-mono text-[11px] text-slate-400/70 group-hover:text-amber-500/70">
            {{ item.index }}
          </span>
          {{ item.label }}
        </RouterLink>
      </nav>

      <div class="space-y-1 border-t border-ink-line px-3 py-4">
        <button
          class="flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-slate-600 transition-colors hover:bg-ink-line/40 dark:text-slate-400 dark:hover:bg-ink-line/60"
          @click="toggle"
        >
          <span class="font-mono text-[11px] text-slate-400/70">TT</span>
          {{ theme === 'dark' ? '浅色模式' : '深色模式' }}
        </button>
        <button
          class="flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-slate-600 transition-colors hover:bg-ink-line/40 dark:text-slate-400 dark:hover:bg-ink-line/60"
          @click="logout"
        >
          <span class="font-mono text-[11px] text-slate-400/70">XX</span>
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
        class="pointer-events-auto flex items-start justify-between gap-3 rounded-md border px-4 py-3 text-sm shadow-panel animate-toastIn"
        :class="
          t.type === 'success'
            ? 'border-emerald-500/40 bg-white text-slate-800 dark:bg-ink-raised dark:text-slate-100'
            : 'border-red-500/40 bg-white text-slate-800 dark:bg-ink-raised dark:text-slate-100'
        "
      >
        <span class="flex items-center gap-2">
          <span
            class="mt-0.5 h-2 w-2 shrink-0 rounded-full"
            :class="t.type === 'success' ? 'bg-emerald-500' : 'bg-red-500'"
          />
          <span>{{ t.text }}</span>
        </span>
        <button class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200" @click="remove(t.id)">
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
