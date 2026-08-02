import { createRouter, createWebHistory } from 'vue-router'
import { api, authState } from './api/client'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    { path: '/', component: () => import('./views/OverviewView.vue') },
    { path: '/keys', component: () => import('./views/UpstreamKeysView.vue') },
    { path: '/api-keys', component: () => import('./views/ApiKeysView.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  // 启动时先确认登录态（仅一次）。
  if (!authState.ready) {
    try {
      authState.authenticated = (await api.session()).authenticated
    } catch {
      authState.authenticated = false
    }
    authState.ready = true
  }
  if (to.meta.public) {
    return authState.authenticated ? '/' : true
  }
  return authState.authenticated ? true : '/login'
})

export default router
