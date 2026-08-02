<script setup lang="ts">
import { ref } from 'vue'
import { api } from '../api/client'
import type { ProxyKey, ProxyKeyCreated } from '../api/types'
import { usePolling } from '../composables/usePolling'
import { useToast } from '../composables/useToast'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import RevealKeyDialog from '../components/RevealKeyDialog.vue'

const keys = ref<ProxyKey[]>([])
const { push } = useToast()

const formOpen = ref(false)
const newName = ref('')
const creating = ref(false)

const revoking = ref<ProxyKey | null>(null)
const revealed = ref<ProxyKeyCreated | null>(null)

async function load() {
  keys.value = await api.proxyKeys()
}
usePolling(load, 10000)

async function create() {
  if (creating.value) return
  creating.value = true
  try {
    const created = await api.createProxyKey(newName.value.trim())
    keys.value.unshift(created)
    formOpen.value = false
    newName.value = ''
    // 弹窗展示一次明文（关闭即销毁，之后无处可查）。
    revealed.value = created
  } catch (e) {
    push('error', (e as Error).message)
  } finally {
    creating.value = false
  }
}

async function confirmRevoke() {
  if (!revoking.value) return
  try {
    await api.deleteProxyKey(revoking.value.id)
    const idx = keys.value.findIndex((x) => x.id === revoking.value!.id)
    if (idx >= 0) keys.value[idx] = { ...keys.value[idx], revoked: true }
    push('success', `已吊销「${revoking.value.name}」`)
  } catch (e) {
    push('error', (e as Error).message)
  } finally {
    revoking.value = null
  }
}

function fmtTime(s: string | null): string {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { hour12: false })
}
</script>

<template>
  <div class="max-w-6xl animate-rise">
    <header class="flex items-center justify-between">
      <h1 class="font-mono text-xl font-semibold tracking-wide text-slate-800 dark:text-slate-100">
        API Key
      </h1>
      <button
        class="rounded-md bg-amber-500 px-4 py-2 text-sm font-semibold text-black transition-colors hover:bg-amber-400"
        @click="formOpen = true"
      >
        + 创建 API Key
      </button>
    </header>

    <div
      v-if="formOpen"
      class="mt-6 rounded-lg border border-amber-400/40 bg-white p-5 shadow-panel dark:bg-ink-raised"
    >
      <div class="flex gap-4">
        <input
          v-model="newName"
          placeholder="给调用方起个名字，如：本地脚本 / CI"
          class="flex-1 rounded-md border border-ink-line bg-slate-50 px-3 py-2.5 text-sm outline-none focus:border-amber-400 dark:bg-ink dark:text-slate-100"
          @keydown.enter="create"
        />
        <button
          :disabled="creating"
          class="rounded-md bg-amber-500 px-4 py-2.5 text-sm font-semibold text-black transition-colors hover:bg-amber-400 disabled:opacity-50"
          @click="create"
        >
          {{ creating ? '创建中…' : '创建' }}
        </button>
        <button
          class="rounded-md border border-ink-line px-4 py-2.5 text-sm text-slate-500 transition-colors hover:bg-slate-100 dark:hover:bg-ink-line/60"
          @click="formOpen = false"
        >
          取消
        </button>
      </div>
    </div>

    <div class="mt-6 overflow-x-auto rounded-lg border border-ink-line bg-white shadow-panel dark:bg-ink-raised">
      <table class="w-full min-w-[820px] text-left text-sm">
        <thead>
          <tr class="border-b border-ink-line text-[11px] uppercase tracking-widest text-slate-400 dark:text-slate-500">
            <th class="px-4 py-3 font-medium">名称</th>
            <th class="px-4 py-3 font-medium">前缀</th>
            <th class="px-4 py-3 font-medium">调用数</th>
            <th class="px-4 py-3 font-medium">最后使用</th>
            <th class="px-4 py-3 font-medium">创建时间</th>
            <th class="px-4 py-3 font-medium">状态</th>
            <th class="px-4 py-3 text-right font-medium">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="k in keys"
            :key="k.id"
            class="border-b border-ink-line/60 last:border-0 hover:bg-slate-50/60 dark:hover:bg-ink-line/30"
            :class="k.revoked ? 'opacity-50' : ''"
          >
            <td class="px-4 py-3 font-medium text-slate-800 dark:text-slate-100">{{ k.name }}</td>
            <td class="num px-4 py-3 text-slate-500 dark:text-slate-400">{{ k.key_prefix }}…</td>
            <td class="num px-4 py-3 text-slate-500 dark:text-slate-400">{{ k.request_count }}</td>
            <td class="num px-4 py-3 text-xs text-slate-400">{{ fmtTime(k.last_used_at) }}</td>
            <td class="num px-4 py-3 text-xs text-slate-400">{{ fmtTime(k.created_at) }}</td>
            <td class="px-4 py-3">
              <span
                class="inline-flex items-center gap-1.5 text-xs font-medium"
                :class="k.revoked ? 'text-slate-400' : 'text-emerald-600 dark:text-emerald-400'"
              >
                <span class="h-1.5 w-1.5 rounded-full" :class="k.revoked ? 'bg-slate-400' : 'bg-emerald-500'" />
                {{ k.revoked ? '已吊销' : '正常' }}
              </span>
            </td>
            <td class="px-4 py-3">
              <div class="flex justify-end">
                <button
                  v-if="!k.revoked"
                  class="rounded border border-red-500/40 px-2 py-1 text-xs text-red-500 transition-colors hover:bg-red-500/10"
                  @click="revoking = k"
                >
                  吊销
                </button>
                <span v-else class="text-xs text-slate-300 dark:text-slate-600">已吊销</span>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-if="keys.length === 0" class="p-6 text-center text-sm text-slate-400">
        还没有 API Key——点击右上角「创建 API Key」，调用方将使用它访问代理。
      </p>
    </div>

    <ConfirmDialog
      :open="revoking !== null"
      title="吊销 API Key？"
      :message="`吊销「${revoking?.name ?? ''}」后，使用它的调用方将立即收到 401。可随时重新创建。`"
      confirm-text="确认吊销"
      danger
      @confirm="confirmRevoke"
      @cancel="revoking = null"
    />

    <RevealKeyDialog
      v-if="revealed"
      :open="true"
      :name="revealed.name"
      :plaintext="revealed.plaintext_key"
      @close="revealed = null"
    />
  </div>
</template>
