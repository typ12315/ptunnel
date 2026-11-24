import { createRouter, createWebHistory } from 'vue-router'
import api from './api'

const routes = [
  { path: '/login', component: () => import('./views/Login.vue'), meta: { public: true } },
  { path: '/', component: () => import('./views/Dashboard.vue') },
  { path: '/clients', component: () => import('./views/Clients.vue') },
  { path: '/proxies', component: () => import('./views/Proxies.vue') },
  { path: '/connections', component: () => import('./views/Connections.vue') },
  { path: '/sql-audit', component: () => import('./views/SqlAudit.vue') },
  { path: '/audit', component: () => import('./views/Audit.vue') },
  { path: '/settings', component: () => import('./views/Settings.vue') }
]

const router = createRouter({ history: createWebHistory(), routes })

export const userState = { username: '', role: '' }

router.beforeEach(async (to) => {
  if (to.meta.public) return true
  try {
    const { data } = await api.get('/api/auth/me')
    userState.username = data.username
    userState.role = data.role
    return true
  } catch (e) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
})

export default router