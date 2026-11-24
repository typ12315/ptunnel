<template>
  <div>
    <div class="toolbar">
      <el-button type="primary" @click="openCreate">新建客户端</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="muted">共 {{ items.length }} 个客户端</span>
    </div>

    <el-card shadow="never">
      <el-table :data="items" v-loading="loading">
        <el-table-column label="名称" min-width="140">
          <template #default="{ row }">
            <div>{{ row.name }}</div>
            <div class="muted mono">{{ row.id }}</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag v-if="!row.enabled" type="info" size="small">已禁用</el-tag>
            <el-tag v-else-if="row.online" type="success" size="small">在线</el-tag>
            <el-tag v-else type="danger" size="small">离线</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="版本 / 地址" min-width="150">
          <template #default="{ row }">
            <div>{{ row.version || '-' }}</div>
            <div class="muted mono">{{ row.remote_addr || '-' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="代理数" width="90" prop="proxy_count" />
        <el-table-column label="待审批" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.pending_count" type="warning" size="small">{{ row.pending_count }}</el-tag>
            <span v-else class="muted">0</span>
          </template>
        </el-table-column>
        <el-table-column label="最后心跳" width="170">
          <template #default="{ row }">{{ formatTime(row.last_seen_at) }}</template>
        </el-table-column>
        <el-table-column label="备注" min-width="120" prop="remark" />
        <el-table-column label="操作" width="280" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="showToken(row)">注册令牌</el-button>
            <el-button link type="primary" @click="toggle(row)">{{ row.enabled ? '禁用' : '启用' }}</el-button>
            <el-button link type="warning" @click="revoke(row)">吊销证书</el-button>
            <el-button link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="createVisible" title="新建客户端" width="440px">
      <el-form :model="form" label-width="80px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="例如 web01" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.remark" placeholder="可选" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="create">创建并生成令牌</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="tokenVisible" title="客户端注册令牌" width="560px">
      <el-alert type="warning" :closable="false" show-icon title="令牌仅显示一次，15 分钟内有效" description="将下面的配置片段写入客户端 client.yaml 后启动客户端。" />
      <pre class="mono code">{{ tokenConfig }}</pre>
      <div class="muted">令牌明文：</div>
      <el-input :model-value="token" readonly>
        <template #append>
          <el-button @click="copy(token)">复制</el-button>
        </template>
      </el-input>
      <template #footer>
        <el-button type="primary" @click="tokenVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import api, { errText } from '../api'
import { formatTime } from '../utils'

const items = ref([])
const loading = ref(false)
const saving = ref(false)
const createVisible = ref(false)
const tokenVisible = ref(false)
const token = ref('')
const tokenClient = ref(null)
const form = reactive({ name: '', remark: '' })

const tokenConfig = computed(() => {
  if (!tokenClient.value) return ''
  return [
    'server:',
    `  addr: <服务端地址:8443>`,
    '  ca: /etc/ptunnel/ca.pem',
    '  cert: /etc/ptunnel/client.crt',
    '  key: /etc/ptunnel/client.key',
    `  name: ${tokenClient.value.name}`,
    `  enroll_token: ${token.value}`
  ].join('\n')
})

async function load() {
  loading.value = true
  try {
    const { data } = await api.get('/api/clients')
    items.value = data.items || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.name = ''
  form.remark = ''
  createVisible.value = true
}

async function create() {
  if (!form.name) {
    ElMessage.warning('请填写名称')
    return
  }
  saving.value = true
  try {
    const { data } = await api.post('/api/clients', form)
    createVisible.value = false
    token.value = data.enroll_token
    tokenClient.value = data.client
    tokenVisible.value = true
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    saving.value = false
  }
}

async function showToken(row) {
  try {
    const { data } = await api.post(`/api/clients/${row.id}/enroll-token`)
    token.value = data.enroll_token
    tokenClient.value = row
    tokenVisible.value = true
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function toggle(row) {
  try {
    await api.patch(`/api/clients/${row.id}`, { enabled: !row.enabled })
    ElMessage.success(row.enabled ? '已禁用' : '已启用')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function revoke(row) {
  try {
    await ElMessageBox.confirm(`确定吊销客户端 ${row.name} 的证书？在线连接会被立即断开。`, '吊销证书', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.post(`/api/clients/${row.id}/revoke-cert`)
    ElMessage.success('证书已吊销')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(`删除客户端 ${row.name} 会同时删除其全部代理规则，确定继续？`, '删除客户端', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.delete(`/api/clients/${row.id}`)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

onMounted(load)
</script>

<style scoped>
.code { background: #f5f7fa; padding: 12px; border-radius: 6px; white-space: pre-wrap; margin: 12px 0; }
</style>