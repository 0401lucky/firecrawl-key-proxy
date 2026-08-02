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
        <div class="mx-auto grid h-14 w-14 place-items-center rounded-lg bg-amber-400 font-mono text-2xl font-bold text-slate-950 shadow-lamp">
          F
        </div>
        <h1 class="mt-4 font-mono text-lg font-semibold tracking-[0.3em] t-primary">
          FIRECRAWL · 面板
        </h1>
        <p class="mt-1.5 text-xs t-muted">多 Key 反向代理管理控制台</p>
      </div>

      <form class="surface p-6" @submit.prevent="submit">
        <label class="block text-xs font-semibold t-secondary" for="pw">管理员密码</label>
        <input
          id="pw"
          v-model="password"
          type="password"
          autofocus
          autocomplete="current-password"
          placeholder="输入 ADMIN_PASSWORD"
          class="field mt-2 w-full"
          @keydown.enter="submit"
        />
        <p v-if="error" class="mt-2 text-xs text-red-600 dark:text-red-400">{{ error }}</p>
        <button type="submit" :disabled="submitting" class="btn-primary mt-5 w-full">
          {{ submitting ? '验证中…' : '进入面板' }}
        </button>
      </form>
    </div>
  </div>
</template>
