<template>
  <div>
    <div class="toolbar">
      <el-input v-model="action" placeholder="按动作前缀筛选，如 client." clearable style="width: 240px" @keyup.enter="search" />
      <el-button type="primary" @click="search">查询</el-button>
      <el-button @click="load">刷新</el-button>
      <span class="muted">共 {{ total }} 条</span>
    </div>

    <el-card shadow="never">
      <el-table :data="items" v-loading="loading">
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作者" width="140">
          <template #default="{ row }">{{ row.actor || '-' }}<div class="muted mono">{{ row.ip }}</div></template>
        </el-table-column>
        <el-table-column label="动作" width="180">
          <template #default="{ row }"><span class="mono">{{ row.action }}</span></template>
        </el-table-column>
        <el-table-column label="对象" width="160">
          <template #default="{ row }">{{ row.entity || '-' }}</template>
        </el-table-column>
        <el-table-column label="详情" min-width="240" prop="detail" />
      </el-table>
      <el-pagination
        style="margin-top: 16px; justify-content: flex-end"
        layout="total, prev, pager, next"
        :total="total"
        :page-size="size"
        :current-page="page"
        @current-change="onPage"
      />
    </el-card>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import api, { errText } from '../api'
import { formatTime } from '../utils'

const items = ref([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
const action = ref('')
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const { data } = await api.get('/api/audit', { params: { page: page.value, size: size.value, action: action.value } })
    items.value = data.items || []
    total.value = data.total || 0
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}

function onPage(p) {
  page.value = p
  load()
}

onMounted(load)
</script>