<template>
  <router-view v-if="isPublic" />
  <el-container v-else class="layout">
    <el-aside width="208px" class="aside">
      <div class="brand">PortTunnel</div>
      <el-menu :default-active="$route.path" router class="menu" background-color="#1f2d3d" text-color="#c0c4cc" active-text-color="#409eff">
        <el-menu-item index="/">仪表盘</el-menu-item>
        <el-menu-item index="/clients">客户端</el-menu-item>
        <el-menu-item index="/proxies">端口代理</el-menu-item>
        <el-menu-item index="/connections">连接审计</el-menu-item>
        <el-menu-item index="/sql-audit">SQL 审计</el-menu-item>
        <el-menu-item index="/audit">操作审计</el-menu-item>
        <el-menu-item index="/settings">系统设置</el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="header">
        <div class="title">{{ pageTitle }}</div>
        <div class="user">
          <span>{{ username }}</span>
          <el-button link type="primary" @click="logout">退出登录</el-button>
        </div>
      </el-header>
      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup>
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import api from './api'
import { userState } from './router'

const route = useRoute()
const router = useRouter()

const isPublic = computed(() => !!route.meta.public)
const username = computed(() => userState.username || '')
const titles = {
  '/': '仪表盘',
  '/clients': '客户端管理',
  '/proxies': '端口代理',
  '/connections': '连接审计',
  '/sql-audit': 'SQL 审计',
  '/audit': '操作审计',
  '/settings': '系统设置'
}
const pageTitle = computed(() => titles[route.path] || '')

async function logout() {
  await api.post('/api/auth/logout').catch(() => {})
  location.href = '/login'
}
</script>

<style>
html, body, #app { height: 100%; margin: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Microsoft YaHei', sans-serif; background: #f5f7fa; }
.layout { height: 100%; }
.aside { background: #1f2d3d; }
.brand { color: #fff; font-size: 18px; font-weight: 600; padding: 18px 20px; letter-spacing: 1px; }
.menu { border-right: none; }
.header { background: #fff; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #ebeef5; height: 56px; }
.title { font-size: 16px; font-weight: 600; }
.user span { margin-right: 12px; color: #606266; }
.main { padding: 20px; }
.toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 16px; flex-wrap: wrap; }
.stat-grid { display: grid; grid-template-columns: repeat(4, minmax(180px, 1fr)); gap: 16px; margin-bottom: 16px; }
.stat-card { background: #fff; border-radius: 8px; padding: 18px; border: 1px solid #ebeef5; }
.stat-card .label { color: #909399; font-size: 13px; }
.stat-card .value { font-size: 26px; font-weight: 600; margin-top: 8px; color: #303133; }
.stat-card .sub { color: #909399; font-size: 12px; margin-top: 6px; }
.runtime-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; }
.runtime-grid .stat-card { padding: 14px; }
.runtime-grid .stat-card .value { font-size: 22px; }
.table-footer { display: flex; justify-content: flex-end; margin-top: 10px; }
.mono { font-family: Consolas, Monaco, monospace; font-size: 12px; }
.muted { color: #909399; }
</style>