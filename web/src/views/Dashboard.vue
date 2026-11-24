<template>
  <div>
    <div class="stat-grid">
      <div class="stat-card">
        <div class="label">在线客户端</div>
        <div class="value">{{ overview.clients_online ?? 0 }} / {{ overview.clients_total ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="label">监听端口</div>
        <div class="value">{{ overview.listeners ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="label">当前活跃连接</div>
        <div class="value">{{ overview.active_conns ?? 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="label">今日连接 / 流量</div>
        <div class="value">
          {{ overview.today_conns ?? 0 }}
          <span class="muted" style="font-size: 13px">
            / {{ formatBytes((overview.today_bytes_in || 0) + (overview.today_bytes_out || 0)) }}
          </span>
        </div>
      </div>
    </div>

    <el-card shadow="never" style="margin-bottom: 16px">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center">
          <span>近 24 小时连接与流量</span>
          <el-radio-group v-model="hours" size="small" @change="loadSeries">
            <el-radio-button :value="6">6 小时</el-radio-button>
            <el-radio-button :value="24">24 小时</el-radio-button>
            <el-radio-button :value="72">3 天</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <div ref="chartEl" style="height: 280px"></div>
    </el-card>

    <el-card shadow="never" style="margin-bottom: 16px">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center">
          <span>运维检测</span>
          <div style="display: flex; align-items: center; gap: 10px">
            <span class="muted" style="font-size: 12px">更新于 {{ runtimeUpdated || '-' }}</span>
            <el-button size="small" @click="loadRuntime">刷新</el-button>
          </div>
        </div>
      </template>

      <div class="runtime-grid">
        <div class="stat-card">
          <div class="label">CPU 使用率</div>
          <div class="value">{{ (runtime.process?.cpu_percent ?? 0).toFixed(1) }}%</div>
          <div class="sub">累计 {{ (runtime.process?.cpu_seconds ?? 0).toFixed(1) }} s</div>
        </div>
        <div class="stat-card">
          <div class="label">进程内存</div>
          <div class="value">{{ formatBytes(runtime.process?.rss_bytes || 0) }}</div>
          <div class="sub">Heap {{ formatBytes(runtime.go?.heap_alloc_bytes || 0) }} / Sys {{ formatBytes(runtime.go?.sys_bytes || 0) }}</div>
        </div>
        <div class="stat-card">
          <div class="label">Goroutine / 线程</div>
          <div class="value">{{ runtime.go?.goroutines ?? 0 }} / {{ runtime.process?.threads ?? 0 }}</div>
          <div class="sub">句柄 {{ runtime.process?.handles ?? 0 }} · GC {{ runtime.go?.num_gc ?? 0 }} 次</div>
        </div>
        <div class="stat-card">
          <div class="label">运行时长</div>
          <div class="value">{{ formatDuration((runtime.uptime_seconds || 0) * 1000) }}</div>
          <div class="sub">{{ runtime.go?.version || '-' }}</div>
        </div>
        <div class="stat-card">
          <div class="label">隧道会话 / 监听</div>
          <div class="value">{{ runtime.tunnel?.clients_online ?? 0 }} / {{ runtime.tunnel?.listeners ?? 0 }}</div>
          <div class="sub">累计连接 {{ runtime.tunnel?.total_conns ?? 0 }} · 拒绝 {{ runtime.tunnel?.rejected_conns ?? 0 }}</div>
        </div>
        <div class="stat-card">
          <div class="label">审计队列 / 丢弃</div>
          <div class="value">{{ runtime.tunnel?.audit_queue ?? 0 }} / {{ runtime.tunnel?.audit_dropped ?? 0 }}</div>
          <div class="sub">累计流量 {{ formatBytes((runtime.tunnel?.bytes_in || 0) + (runtime.tunnel?.bytes_out || 0)) }}</div>
        </div>
      </div>

      <el-tabs v-model="runtimeTab" style="margin-top: 8px">
        <el-tab-pane :label="`活跃连接 (${connections.length})`" name="conns">
          <el-table :data="pagedConns" size="small" max-height="360">
            <el-table-column label="开始时间" width="170">
              <template #default="{ row }">{{ formatTime(row.started_at) }}</template>
            </el-table-column>
            <el-table-column label="来源" width="150">
              <template #default="{ row }">{{ row.src_ip }}:{{ row.src_port }}</template>
            </el-table-column>
            <el-table-column label="监听地址" width="150" prop="listen_addr" />
            <el-table-column label="客户端 / 代理" min-width="170">
              <template #default="{ row }">{{ row.client_name }} / {{ row.proxy_name }}</template>
            </el-table-column>
            <el-table-column label="目标地址" min-width="160" prop="target_addr" />
            <el-table-column label="已运行" width="100">
              <template #default="{ row }">{{ formatDuration(row.duration_ms) }}</template>
            </el-table-column>
            <el-table-column label="上行" width="100">
              <template #default="{ row }">{{ formatBytes(row.bytes_in) }}</template>
            </el-table-column>
            <el-table-column label="下行" width="100">
              <template #default="{ row }">{{ formatBytes(row.bytes_out) }}</template>
            </el-table-column>
          </el-table>
          <div class="table-footer">
            <el-pagination
              v-model:current-page="connPage"
              :page-size="connPageSize"
              :total="connections.length"
              layout="total, prev, pager, next"
              size="small"
              background
            />
          </div>
        </el-tab-pane>

        <el-tab-pane :label="`隧道会话 (${sessions.length})`" name="sessions">
          <el-table :data="sessions" size="small" max-height="360">
            <el-table-column label="客户端" min-width="150" prop="client_name" />
            <el-table-column label="远程地址" min-width="160" prop="remote_addr" />
            <el-table-column label="在线时长" width="120">
              <template #default="{ row }">{{ formatDuration(Date.now() - new Date(row.since).getTime()) }}</template>
            </el-table-column>
            <el-table-column label="活跃流" width="90" prop="streams" />
            <el-table-column label="配置版本" width="100" prop="revision" />
            <el-table-column label="最后心跳" width="170">
              <template #default="{ row }">{{ formatTime(row.last_pong) }}</template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <el-tab-pane :label="`监听端口 (${listeners.length})`" name="listeners">
          <el-table :data="listeners" size="small" max-height="360">
            <el-table-column label="监听地址" width="170">
              <template #default="{ row }">{{ row.bind_addr }}:{{ row.port }}</template>
            </el-table-column>
            <el-table-column label="代理" min-width="150" prop="proxy_name" />
            <el-table-column label="协议" width="90" prop="protocol" />
            <el-table-column label="目标地址" min-width="170" prop="target_addr" />
            <el-table-column label="活跃连接" width="100" prop="active_conns" />
          </el-table>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div style="display: flex; justify-content: space-between; align-items: center">
          <span>最近访问</span>
          <el-button link type="primary" @click="$router.push('/connections')">查看全部</el-button>
        </div>
      </template>
      <el-table :data="recent" size="small">
        <el-table-column label="开始时间" width="170">
          <template #default="{ row }">{{ formatTime(row.started_at) }}</template>
        </el-table-column>
        <el-table-column label="来源" width="150">
          <template #default="{ row }">{{ row.src_ip }}:{{ row.src_port }}</template>
        </el-table-column>
        <el-table-column label="服务端端口" width="110" prop="remote_port" />
        <el-table-column label="客户端 / 代理" min-width="180">
          <template #default="{ row }">{{ row.client_name }} / {{ row.proxy_name }}</template>
        </el-table-column>
        <el-table-column label="目标地址" min-width="170" prop="target_addr" />
        <el-table-column label="时长" width="110">
          <template #default="{ row }">{{ formatDuration(row.duration_ms) }}</template>
        </el-table-column>
        <el-table-column label="结束原因" width="110">
          <template #default="{ row }">
            <el-tag v-if="row.ended_at" size="small" type="info">{{ closeReasonText(row.close_reason) }}</el-tag>
            <el-tag v-else size="small" type="success">进行中</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import * as echarts from 'echarts'
import api from '../api'
import { closeReasonText, formatBytes, formatDuration, formatTime } from '../utils'

const overview = ref({})
const recent = ref([])
const hours = ref(24)
const chartEl = ref(null)
let chart = null
let timer = null
let runtimeTimer = null

const runtime = ref({})
const sessions = ref([])
const listeners = ref([])
const connections = ref([])
const runtimeUpdated = ref('')
const runtimeTab = ref('conns')
const connPage = ref(1)
const connPageSize = 20
const pagedConns = computed(() => {
  const start = (connPage.value - 1) * connPageSize
  return connections.value.slice(start, start + connPageSize)
})

async function loadOverview() {
  const { data } = await api.get('/api/stats/overview')
  overview.value = data
}

async function loadRecent() {
  const { data } = await api.get('/api/connections', { params: { size: 10 } })
  recent.value = data.items || []
}

async function loadRuntime() {
  const { data } = await api.get('/api/system/runtime')
  runtime.value = data
  sessions.value = data.sessions || []
  listeners.value = data.listeners || []
  connections.value = data.connections || []
  runtimeUpdated.value = formatTime(data.time)
  const maxPage = Math.max(1, Math.ceil(connections.value.length / connPageSize))
  if (connPage.value > maxPage) connPage.value = maxPage
}

async function loadSeries() {
  const { data } = await api.get('/api/stats/series', { params: { hours: hours.value } })
  const items = data.items || []
  if (!chart) return
  chart.setOption({
    tooltip: { trigger: 'axis' },
    legend: { data: ['连接数', '上行', '下行'] },
    grid: { left: 50, right: 60, top: 40, bottom: 30 },
    xAxis: { type: 'category', data: items.map((i) => formatTime(i.ts).slice(5, 16)) },
    yAxis: [
      { type: 'value', name: '连接数' },
      { type: 'value', name: '字节' }
    ],
    series: [
      { name: '连接数', type: 'bar', data: items.map((i) => i.conns), barMaxWidth: 18 },
      { name: '上行', type: 'line', yAxisIndex: 1, smooth: true, data: items.map((i) => i.bytes_in) },
      { name: '下行', type: 'line', yAxisIndex: 1, smooth: true, data: items.map((i) => i.bytes_out) }
    ]
  })
}

onMounted(async () => {
  chart = echarts.init(chartEl.value)
  await Promise.all([loadOverview(), loadRecent(), loadRuntime(), loadSeries()])
  timer = setInterval(() => {
    loadOverview()
    loadRecent()
  }, 10000)
  runtimeTimer = setInterval(loadRuntime, 5000)
  window.addEventListener('resize', resize)
})

function resize() {
  chart && chart.resize()
}

onBeforeUnmount(() => {
  clearInterval(timer)
  clearInterval(runtimeTimer)
  window.removeEventListener('resize', resize)
  chart && chart.dispose()
})
</script>