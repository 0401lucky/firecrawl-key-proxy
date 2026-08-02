<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, authState, ApiError } from '../api/client'

const router = useRouter()
const password = ref('')
const error = ref('')
const submitting = ref(false)

async function submit() {
  if (submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    await api.login(password.value)
    authState.authenticated = true
    router.push('/')
  } catch (e) {
    // 统一提示，不区分「密码错」与其他失败原因。
    error.value = e instanceof ApiError ? e.message : '登录失败，请重试'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center p-6">
    <div class="w-full max-w-sm animate-rise">
      <div class="mb-8 text-center">
        <div class="mx-auto grid h-14 w-14 place-items-center rounded-lg bg-amber-400 font-mono text-2xl font-bold text-black shadow-lamp">
          F
        </div>
        <h1 class="mt-4 font-mono text-lg font-semibold tracking-[0.3em] text-slate-800 dark:text-slate-100">
          FIRECRAWL · 面板
        </h1>
        <p class="mt-1 text-xs text-slate-400 dark:text-slate-500">多 Key 反向代理管理控制台</p>
      </div>

      <form
        class="rounded-lg border border-ink-line bg-white p-6 shadow-panel dark:bg-ink-raised"
        @submit.prevent="submit"
      >
        <label class="block text-xs font-medium text-slate-500 dark:text-slate-400" for="pw">
          管理员密码
        </label>
        <input
          id="pw"
          v-model="password"
          type="password"
          autofocus
          autocomplete="current-password"
          placeholder="输入 ADMIN_PASSWORD"
          class="mt-2 w-full rounded-md border border-ink-line bg-slate-50 px-3 py-2.5 text-sm text-slate-800 outline-none transition-colors focus:border-amber-400 dark:bg-ink dark:text-slate-100"
          @keydown.enter="submit"
        />
        <p v-if="error" class="mt-2 text-xs text-red-500">{{ error }}</p>
        <button
          type="submit"
          :disabled="submitting"
          class="mt-5 w-full rounded-md bg-amber-500 py-2.5 text-sm font-semibold text-black transition-colors hover:bg-amber-400 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {{ submitting ? '验证中…' : '进入面板' }}
        </button>
      </form>
    </div>
  </div>
</template>
