<template>
  <div>
    <div class="toolbar">
      <el-select v-model="filter.date" style="width: 150px" @change="search">
        <el-option v-for="d in dates" :key="d" :label="d === today ? `${d}（今天）` : d" :value="d" />
      </el-select>
      <el-input v-model="filter.ip" placeholder="来源 IP" clearable style="width: 150px" @keyup.enter="search" />
      <el-input v-model="filter.mac" placeholder="来源 MAC" clearable style="width: 170px" @keyup.enter="search" />
      <el-select v-model="filter.app" placeholder="全部应用" clearable style="width: 150px" @change="search">
        <el-option v-for="o in appOptions" :key="o.value" :label="o.label" :value="o.value" />
      </el-select>
      <el-input v-model="filter.keyword" placeholder="SQL 关键字" clearable style="width: 180px" @keyup.enter="search" />
      <el-button type="primary" @click="search">查询</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="muted">共 {{ total }} 条</span>
    </div>

    <el-alert
      v-if="!enabled"
      type="warning"
      :closable="false"
      show-icon
      title="SQL 语句审计未启用"
      description="服务端 audit.sql_log_enabled 为 false。启用后，只有绑定了应用类型的代理才会记录语句。"
      style="margin-bottom: 16px"
    />
    <el-alert
      v-else-if="!items.length && !loading"
      type="info"
      :closable="false"
      show-icon
      title="没有匹配的记录"
      description="只有绑定了应用类型（MySQL/PostgreSQL/...) 的代理会解析语句；数据库客户端启用 TLS 加密后无法解析。"
      style="margin-bottom: 16px"
    />

    <el-card shadow="never">
      <el-table :data="items" v-loading="loading" @row-click="showDetail" style="cursor: pointer">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(row.time) }}</template>
        </el-table-column>
        <el-table-column label="来源 IP" width="140">
          <template #default="{ row }"><span class="mono">{{ row.src_ip }}:{{ row.src_port }}</span></template>
        </el-table-column>
        <el-table-column label="来源 MAC" width="150">
          <template #default="{ row }">
            <span v-if="row.src_mac" class="mono">{{ row.src_mac }}</span>
            <span v-else class="muted" title="跨网段/公网访问无法获取 MAC">—</span>
          </template>
        </el-table-column>
        <el-table-column label="客户端 / 代理" width="180">
          <template #default="{ row }">
            <div>{{ row.client_name }}</div>
            <div class="muted">{{ row.proxy_name }}</div>
          </template>
        </el-table-column>
        <el-table-column label="应用" width="120">
          <template #default="{ row }">
            <el-tag size="small" type="success">{{ appLabel(row.app_type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="目标" width="150">
          <template #default="{ row }"><span class="mono">{{ row.target_addr }}</span></template>
        </el-table-column>
        <el-table-column label="SQL / 命令" min-width="320">
          <template #default="{ row }">
            <span class="mono sql-preview">{{ row.sql }}</span>
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

    <el-drawer v-model="drawer" title="SQL 审计详情" size="560px">
      <el-descriptions v-if="detail" :column="1" border>
        <el-descriptions-item label="时间">{{ formatTime(detail.time) }}</el-descriptions-item>
        <el-descriptions-item label="来源地址">{{ detail.src_ip }}:{{ detail.src_port }}</el-descriptions-item>
        <el-descriptions-item label="来源 MAC">
          <span v-if="detail.src_mac" class="mono">{{ detail.src_mac }}</span>
          <span v-else class="muted">未获取（跨网段/公网访问无 MAC）</span>
        </el-descriptions-item>
        <el-descriptions-item label="客户端">{{ detail.client_name }}</el-descriptions-item>
        <el-descriptions-item label="代理">{{ detail.proxy_name }}（{{ detail.proxy_id }}）</el-descriptions-item>
        <el-descriptions-item label="应用类型">{{ appLabel(detail.app_type) }}</el-descriptions-item>
        <el-descriptions-item label="转发目标">{{ detail.target_addr }}</el-descriptions-item>
        <el-descriptions-item label="语句类型">{{ kindLabel(detail.kind) }}</el-descriptions-item>
        <el-descriptions-item label="连接 ID"><span class="mono">{{ detail.conn_id }}</span></el-descriptions-item>
      </el-descriptions>
      <div style="margin-top: 16px">
        <div class="muted" style="margin-bottom: 6px">语句内容</div>
        <pre class="sql-block">{{ detail ? detail.sql : '' }}</pre>
      </div>
    </el-drawer>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import api, { errText } from '../api'
import { formatTime } from '../utils'

const items = ref([])
const total = ref(0)
const dates = ref([])
const today = ref('')
const enabled = ref(true)
const loading = ref(false)
const drawer = ref(false)
const detail = ref(null)

const appOptions = [
  { value: 'mysql', label: 'MySQL' },
  { value: 'postgresql', label: 'PostgreSQL' },
  { value: 'sqlserver', label: 'SQL Server' },
  { value: 'oracle', label: 'Oracle' },
  { value: 'dm', label: '达梦 DM' },
  { value: 'redis', label: 'Redis' },
  { value: 'mongodb', label: 'MongoDB' }
]

function appLabel(v) {
  const o = appOptions.find((x) => x.value === v)
  return o ? o.label : v || '-'
}

function kindLabel(v) {
  return (
    {
      query: '查询',
      prepare: '预编译',
      command: '命令',
      batch: '批处理',
      heuristic: '启发式提取'
    }[v] || v
  )
}

function localToday() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

const filter = reactive({ date: '', ip: '', mac: '', app: '', keyword: '', page: 1, size: 50 })

async function loadDates() {
  try {
    const { data } = await api.get('/api/sql-logs/dates')
    enabled.value = data.enabled !== false
    today.value = data.today || localToday()
    dates.value = data.items && data.items.length ? data.items : [localToday()]
    if (!filter.date) filter.date = dates.value.includes(today.value) ? today.value : dates.value[0]
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

async function load() {
  loading.value = true
  try {
    const params = { date: filter.date, page: filter.page, size: filter.size }
    if (filter.ip) params.ip = filter.ip
    if (filter.mac) params.mac = filter.mac
    if (filter.app) params.app = filter.app
    if (filter.keyword) params.keyword = filter.keyword
    const { data } = await api.get('/api/sql-logs', { params })
    enabled.value = data.enabled !== false
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

function showDetail(row) {
  detail.value = row
  drawer.value = true
}

onMounted(async () => {
  await loadDates()
  await load()
})
</script>

<style scoped>
.sql-preview {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sql-block {
  background: #f5f7fa;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: Consolas, Monaco, monospace;
  font-size: 12px;
  max-height: 60vh;
  overflow: auto;
}
</style>