<template>
  <div>
    <div class="toolbar">
      <el-select v-model="filterClient" placeholder="全部客户端" clearable style="width: 180px" @change="load">
        <el-option v-for="c in clients" :key="c.id" :label="c.name" :value="c.id" />
      </el-select>
      <el-button type="primary" @click="openCreate">新建代理</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="muted">共 {{ items.length }} 条规则</span>
    </div>

    <el-card shadow="never">
      <el-table :data="items" v-loading="loading">
        <el-table-column label="名称" min-width="130">
          <template #default="{ row }">
            <div>{{ row.name }}</div>
            <div class="muted mono">{{ row.id.slice(0, 8) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="客户端" min-width="120">
          <template #default="{ row }">
            {{ row.client_name }}
            <el-tag v-if="!row.online" type="danger" size="small" style="margin-left: 6px">离线</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="服务端端口" width="110">
          <template #default="{ row }">
            <span class="mono">{{ row.bind_addr }}:{{ row.remote_port }}</span>
          </template>
        </el-table-column>
        <el-table-column label="转发目标" min-width="170">
          <template #default="{ row }"><span class="mono">{{ row.target_addr }}</span></template>
        </el-table-column>
        <el-table-column label="应用类型" width="130">
          <template #default="{ row }">
            <el-tag v-if="row.app_type" type="success" size="small">{{ appLabel(row.app_type) }}</el-tag>
            <span v-else class="muted">普通 TCP</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tooltip v-if="row.state === 'error'" :content="row.state_msg">
              <el-tag type="danger" size="small">监听失败</el-tag>
            </el-tooltip>
            <el-tag v-else-if="row.state === 'listening'" type="success" size="small">监听中</el-tag>
            <el-tag v-else type="info" size="small">未启用</el-tag>
            <el-tag v-if="row.approval === 'pending'" type="warning" size="small" style="margin-left: 4px">待审批</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="活跃 / 累计" width="110">
          <template #default="{ row }">
            {{ row.stat.active_conns }} / {{ row.stat.total_conns }}
          </template>
        </el-table-column>
        <el-table-column label="流量 上/下" width="150">
          <template #default="{ row }">
            {{ formatBytes(row.stat.bytes_in) }} / {{ formatBytes(row.stat.bytes_out) }}
          </template>
        </el-table-column>
        <el-table-column label="来源白名单" min-width="150">
          <template #default="{ row }">
            <span v-if="row.allow_cidrs && row.allow_cidrs.length" class="mono">{{ row.allow_cidrs.join(', ') }}</span>
            <span v-else class="muted">不限</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="260" fixed="right">
          <template #default="{ row }">
            <template v-if="row.approval === 'pending'">
              <el-button link type="success" @click="approve(row, 'approve')">通过</el-button>
              <el-button link type="danger" @click="approve(row, 'reject')">拒绝</el-button>
            </template>
            <template v-else>
              <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button link type="primary" @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button>
              <el-button link type="danger" @click="remove(row)">删除</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="visible" :title="editing ? '编辑代理' : '新建代理'" width="560px">
      <el-form :model="form" label-width="110px">
        <el-form-item label="客户端">
          <el-select v-model="form.client_id" :disabled="!!editing" placeholder="选择客户端" style="width: 100%">
            <el-option v-for="c in clients" :key="c.id" :label="c.name" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="例如 mysql-web" />
        </el-form-item>
        <el-form-item label="监听地址">
          <el-input v-model="form.bind_addr" placeholder="0.0.0.0" />
        </el-form-item>
        <el-form-item label="服务端端口">
          <el-input-number v-model="form.remote_port" :min="1" :max="65535" style="width: 180px" />
          <span class="muted" style="margin-left: 10px">访问 server:该端口</span>
        </el-form-item>
        <el-form-item label="转发目标">
          <el-input v-model="form.target_addr" placeholder="10.1.2.3:3306（客户端可访问的地址）" />
        </el-form-item>
        <el-form-item label="应用类型">
          <el-select v-model="form.app_type" style="width: 100%">
            <el-option v-for="o in appOptions" :key="o.value" :label="o.label" :value="o.value" />
          </el-select>
          <div class="muted" style="margin-top: 4px">绑定后会解析并审计 SQL/命令；仅明文连接可解析（启用 TLS 后无法查看语句）</div>
        </el-form-item>
        <el-form-item label="来源 IP 白名单">
          <el-input v-model="cidrsText" type="textarea" :rows="3" placeholder="每行一个 CIDR，留空表示不限制，例如 203.0.113.0/24" />
        </el-form-item>
        <el-form-item label="最大连接数">
          <el-input-number v-model="form.max_conns" :min="0" :max="100000" style="width: 180px" />
          <span class="muted" style="margin-left: 10px">0 表示使用全局默认</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="visible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import api, { errText } from '../api'
import { formatBytes } from '../utils'

const items = ref([])
const clients = ref([])
const filterClient = ref('')
const loading = ref(false)
const saving = ref(false)
const visible = ref(false)
const editing = ref(null)
const cidrsText = ref('')
const appOptions = [
  { value: '', label: '不绑定（普通 TCP）' },
  { value: 'mysql', label: 'MySQL / MariaDB' },
  { value: 'postgresql', label: 'PostgreSQL' },
  { value: 'sqlserver', label: 'SQL Server' },
  { value: 'oracle', label: 'Oracle' },
  { value: 'dm', label: '达梦 DM' },
  { value: 'redis', label: 'Redis' },
  { value: 'mongodb', label: 'MongoDB' }
]
function appLabel(v) {
  const o = appOptions.find((x) => x.value === v)
  return o ? o.label : v
}
const form = reactive({
  client_id: '',
  name: '',
  protocol: 'tcp',
  bind_addr: '0.0.0.0',
  remote_port: 20000,
  target_addr: '',
  app_type: '',
  max_conns: 0,
  enabled: true
})

async function loadClients() {
  const { data } = await api.get('/api/clients')
  clients.value = data.items || []
}

async function load() {
  loading.value = true
  try {
    const { data } = await api.get('/api/proxies', { params: filterClient.value ? { client_id: filterClient.value } : {} })
    items.value = data.items || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    client_id: filterClient.value || (clients.value[0] && clients.value[0].id) || '',
    name: '',
    protocol: 'tcp',
    bind_addr: '0.0.0.0',
    remote_port: 20000,
    target_addr: '',
    app_type: '',
    max_conns: 0,
    enabled: true
  })
  cidrsText.value = ''
  visible.value = true
}

function openEdit(row) {
  editing.value = row
  Object.assign(form, {
    client_id: row.client_id,
    name: row.name,
    protocol: row.protocol,
    bind_addr: row.bind_addr,
    remote_port: row.remote_port,
    target_addr: row.target_addr,
    app_type: row.app_type || '',
    max_conns: row.max_conns,
    enabled: row.enabled
  })
  cidrsText.value = (row.allow_cidrs || []).join('\n')
  visible.value = true
}

async function save() {
  const payload = { ...form, allow_cidrs: cidrsText.value.split('\n').map((s) => s.trim()).filter(Boolean) }
  saving.value = true
  try {
    if (editing.value) {
      const { client_id, ...rest } = payload
      await api.patch(`/api/proxies/${editing.value.id}`, rest)
    } else {
      await api.post('/api/proxies', payload)
    }
    visible.value = false
    ElMessage.success('已保存')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    saving.value = false
  }
}

async function toggle(row) {
  try {
    await api.post(`/api/proxies/${row.id}/${row.enabled ? 'disable' : 'enable'}`)
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function approve(row, action) {
  try {
    await api.post(`/api/proxies/${row.id}/${action}`)
    ElMessage.success(action === 'approve' ? '已通过' : '已拒绝')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(`确定删除代理 ${row.name}？`, '删除代理', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.delete(`/api/proxies/${row.id}`)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

onMounted(async () => {
  await loadClients()
  await load()
})
</script>