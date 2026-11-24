<template>
  <div>
    <div class="toolbar">
      <el-select v-model="filter.client_id" placeholder="全部客户端" clearable style="width: 160px" @change="search">
        <el-option v-for="c in clients" :key="c.id" :label="c.name" :value="c.id" />
      </el-select>
      <el-select v-model="filter.proxy_id" placeholder="全部代理" clearable style="width: 180px" @change="search">
        <el-option v-for="p in proxies" :key="p.id" :label="`${p.name} (${p.remote_port})`" :value="p.id" />
      </el-select>
      <el-input v-model="filter.src_ip" placeholder="来源 IP" clearable style="width: 150px" @keyup.enter="search" />
      <el-input-number v-model="filter.remote_port" :min="0" :max="65535" placeholder="服务端端口" style="width: 150px" />
      <el-date-picker v-model="range" type="datetimerange" start-placeholder="开始时间" end-placeholder="结束时间" style="width: 330px" />
      <el-checkbox v-model="filter.active" @change="search">仅活跃</el-checkbox>
      <el-button type="primary" @click="search">查询</el-button>
      <el-button @click="exportCsv">导出 CSV</el-button>
      <span class="muted" v-if="filter.active">活跃列表每 5 秒自动刷新</span>
    </div>

    <el-card shadow="never">
      <el-table :data="items" v-loading="loading" @row-click="showDetail" style="cursor: pointer">
        <el-table-column label="开始时间" width="170">
          <template #default="{ row }">{{ formatTime(row.started_at) }}</template>
        </el-table-column>
        <el-table-column label="结束时间" width="170">
          <template #default="{ row }">{{ row.ended_at ? formatTime(row.ended_at) : '进行中' }}</template>
        </el-table-column>
        <el-table-column label="时长" width="100">
          <template #default="{ row }">{{ formatDuration(row.duration_ms) }}</template>
        </el-table-column>
        <el-table-column label="来源" width="160">
          <template #default="{ row }"><span class="mono">{{ row.src_ip }}:{{ row.src_port }}</span></template>
        </el-table-column>
        <el-table-column label="来源 MAC" width="150">
          <template #default="{ row }">
            <span v-if="row.src_mac" class="mono">{{ row.src_mac }}</span>
            <span v-else class="muted" :title="'跨网段/公网访问无法获取 MAC'">—</span>
          </template>
        </el-table-column>
        <el-table-column label="服务端端口" width="110" prop="remote_port" />
        <el-table-column label="客户端" width="110" prop="client_name" />
        <el-table-column label="代理" width="120" prop="proxy_name" />
        <el-table-column label="目标地址" min-width="160">
          <template #default="{ row }"><span class="mono">{{ row.target_addr }}</span></template>
        </el-table-column>
        <el-table-column label="上行 / 下行" width="150">
          <template #default="{ row }">{{ formatBytes(row.bytes_in) }} / {{ formatBytes(row.bytes_out) }}</template>
        </el-table-column>
        <el-table-column label="结束原因" width="110">
          <template #default="{ row }">
            <el-tag v-if="!row.ended_at" type="success" size="small">进行中</el-tag>
            <el-tag v-else size="small" type="info">{{ closeReasonText(row.close_reason) }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <el-pagination
        style="margin-top: 16px; justify-content: flex-end"
        layout="total, sizes, prev, pager, next"
        :total="total"
        :page-size="filter.size"
        :current-page="filter.page"
        :page-sizes="[20, 50, 100, 200]"
        @current-change="onPage"
        @size-change="onSize"
      />
    </el-card>

    <el-drawer v-model="drawer" title="连接审计详情" size="480px">
      <el-descriptions v-if="detail" :column="1" border>
        <el-descriptions-item label="连接 ID"><span class="mono">{{ detail.conn_id }}</span></el-descriptions-item>
        <el-descriptions-item label="来源地址">{{ detail.src_ip }}:{{ detail.src_port }}</el-descriptions-item>
        <el-descriptions-item label="来源 MAC">
          <span v-if="detail.src_mac">{{ detail.src_mac }}（{{ detail.mac_source }}）</span>
          <span v-else class="muted">未获取（跨网段/公网访问无 MAC）</span>
        </el-descriptions-item>
        <el-descriptions-item label="服务端端口">{{ detail.listen_addr }}</el-descriptions-item>
        <el-descriptions-item label="客户端">{{ detail.client_name }}</el-descriptions-item>
        <el-descriptions-item label="代理">{{ detail.proxy_name }}（{{ detail.proxy_id }}）</el-descriptions-item>
        <el-descriptions-item label="转发目标">{{ detail.target_addr }}</el-descriptions-item>
        <el-descriptions-item label="开始时间">{{ formatTime(detail.started_at) }}</el-descriptions-item>
        <el-descriptions-item label="结束时间">{{ detail.ended_at ? formatTime(detail.ended_at) : '进行中' }}</el-descriptions-item>
        <el-descriptions-item label="连接时长">{{ formatDuration(detail.duration_ms) }}</el-descriptions-item>
        <el-descriptions-item label="上行字节">{{ formatBytes(detail.bytes_in) }}（{{ detail.bytes_in }}）</el-descriptions-item>
        <el-descriptions-item label="下行字节">{{ formatBytes(detail.bytes_out) }}（{{ detail.bytes_out }}）</el-descriptions-item>
        <el-descriptions-item label="结束原因">{{ closeReasonText(detail.close_reason) }}</el-descriptions-item>
      </el-descriptions>
    </el-drawer>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import api, { errText } from '../api'
import { ElMessage } from 'element-plus'
import { closeReasonText, formatBytes, formatDuration, formatTime } from '../utils'

const items = ref([])
const clients = ref([])
const proxies = ref([])
const total = ref(0)
const loading = ref(false)
const drawer = ref(false)
const detail = ref(null)
const range = ref(null)
let timer = null

const filter = reactive({ client_id: '', proxy_id: '', src_ip: '', remote_port: 0, active: false, page: 1, size: 50 })

function params() {
  const p = { page: filter.page, size: filter.size }
  if (filter.client_id) p.client_id = filter.client_id
  if (filter.proxy_id) p.proxy_id = filter.proxy_id
  if (filter.src_ip) p.src_ip = filter.src_ip
  if (filter.remote_port) p.remote_port = filter.remote_port
  if (filter.active) p.active = 1
  if (range.value && range.value.length === 2) {
    p.from = range.value[0].toISOString()
    p.to = range.value[1].toISOString()
  }
  return p
}

async function load() {
  loading.value = true
  try {
    const url = filter.active ? '/api/active-connections' : '/api/connections'
    const { data } = await api.get(url, { params: params() })
    items.value = data.items || []
    total.value = data.total || 0
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    loading.value = false
  }
}

function search() {
  filter.page = 1
  load()
}

function onPage(p) {
  filter.page = p
  load()
}

function onSize(s) {
  filter.size = s
  filter.page = 1
  load()
}

async function showDetail(row) {
  try {
    const { data } = await api.get(`/api/connections/${row.id}`)
    detail.value = data
    drawer.value = true
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

function exportCsv() {
  const q = new URLSearchParams()
  const p = params()
  Object.keys(p).forEach((k) => {
    if (k !== 'page' && k !== 'size') q.set(k, p[k])
  })
  window.open(`/api/export/connections.csv?${q.toString()}`, '_blank')
}

onMounted(async () => {
  const [c, p] = await Promise.all([api.get('/api/clients'), api.get('/api/proxies')])
  clients.value = c.data.items || []
  proxies.value = p.data.items || []
  await load()
  timer = setInterval(() => {
    if (filter.active) load()
  }, 5000)
})

onBeforeUnmount(() => clearInterval(timer))
</script>