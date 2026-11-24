<template>
  <div class="login-wrap">
    <el-card class="login-card">
      <div class="login-title">PortTunnel 管理台</div>
      <div class="login-sub">端口隧道 · 集中管理</div>
      <el-form :model="form" @submit.prevent="submit">
        <el-form-item>
          <el-input v-model="form.username" placeholder="用户名" size="large" autofocus />
        </el-form-item>
        <el-form-item>
          <el-input v-model="form.password" type="password" placeholder="口令" size="large" show-password @keyup.enter="submit" />
        </el-form-item>
        <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="submit">登录</el-button>
      </el-form>
      <div v-if="error" class="error">{{ error }}</div>
    </el-card>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import api, { errText } from '../api'

const router = useRouter()
const route = useRoute()
const form = reactive({ username: '', password: '' })
const loading = ref(false)
const error = ref('')

async function submit() {
  if (!form.username || !form.password) {
    error.value = '请输入用户名与口令'
    return
  }
  loading.value = true
  error.value = ''
  try {
    await api.post('/api/auth/login', form)
    router.push(route.query.redirect || '/')
  } catch (e) {
    error.value = errText(e)
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-wrap { height: 100%; display: flex; align-items: center; justify-content: center; background: #1f2d3d; }
.login-card { width: 360px; padding: 8px 12px; }
.login-title { font-size: 20px; font-weight: 600; text-align: center; }
.login-sub { color: #909399; font-size: 12px; text-align: center; margin: 6px 0 24px; }
.error { color: #f56c6c; font-size: 13px; margin-top: 12px; text-align: center; }
</style>