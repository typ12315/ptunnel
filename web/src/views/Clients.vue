<template>
  <div>
    <div class="toolbar">
      <el-button type="primary" @click="openCreate">新建客户端</el-button>
      <el-button @click="load">刷新</el-button>
      <el-button @click="openReleases">版本管理</el-button>
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
        <el-table-column label="版本 / 地址" min-width="200">
          <template #default="{ row }">
            <div>
              {{ row.version || '-' }}
              <el-tag v-if="row.upgrade && row.upgrade.status === 'pending'" type="info" size="small" style="margin-left:6px">升级中</el-tag>
              <el-tag v-else-if="row.upgrade && row.upgrade.status === 'failed'" type="danger" size="small" style="margin-left:6px" :title="row.upgrade.message">升级失败</el-tag>
              <el-tag v-else-if="row.upgradable" type="warning" size="small" style="margin-left:6px">可升级 → {{ row.latest_version }}</el-tag>
              <el-tag v-else-if="row.upgrade && row.upgrade.status === 'ok'" type="success" size="small" style="margin-left:6px">已升级</el-tag>
            </div>
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
        <el-table-column label="操作" width="350" fixed="right">
          <template #default="{ row }">
            <el-button link type="success" :disabled="!row.online || !row.upgradable" @click="upgrade(row)">升级</el-button>
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
  <el-dialog v-model="releasesVisible" title="客户端版本管理" width="860px">
      <el-alert type="info" :closable="false" show-icon
        title="上传各平台的客户端编译产物后，列表中版本较低的客户端即可点击升级"
        description="服务端按 版本 + 系统 + 架构 匹配，客户端会自动领取与自身平台一致的包；同一组合只能存在一个包。" />
      <div class="upload-row">
        <el-upload :auto-upload="false" :show-file-list="false" :on-change="onFileChange">
          <el-button>选择文件</el-button>
        </el-upload>
        <span class="muted">{{ upload.filename || '未选择文件' }}</span>
      </div>
      <el-form :inline="true" class="upload-form">
        <el-form-item label="版本">
          <el-input v-model="upload.version" placeholder="0.5.0" style="width: 120px" />
        </el-form-item>
        <el-form-item label="系统">
          <el-select v-model="upload.os" style="width: 110px">
            <el-option label="linux" value="linux" />
          </el-select>
        </el-form-item>
        <el-form-item label="架构">
          <el-select v-model="upload.arch" style="width: 150px">
            <el-option label="amd64 / x86" value="amd64" />
            <el-option label="arm64" value="arm64" />
            <el-option label="arm / armv7" value="arm" />
          </el-select>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="upload.note" style="width: 150px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="uploading" @click="submitRelease">上传</el-button>
        </el-form-item>
      </el-form>
      <el-table :data="releases" size="small" v-loading="releaseLoading" max-height="320">
        <el-table-column label="版本" prop="version" width="110" />
        <el-table-column label="平台" width="140">
          <template #default="{ row }">{{ row.os }} / {{ row.arch }}</template>
        </el-table-column>
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column label="SHA256" min-width="170">
          <template #default="{ row }"><span class="mono muted">{{ row.sha256.slice(0, 16) }}…</span></template>
        </el-table-column>
        <el-table-column label="上传时间" width="170">
          <template #default="{ row }">{{ formatTime(row.uploaded_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button link type="danger" @click="removeRelease(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="releasesVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
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

const releasesVisible = ref(false)
const releases = ref([])
const releaseLoading = ref(false)
const uploading = ref(false)
const upload = reactive({ version: '', os: 'linux', arch: 'amd64', note: '', file: null, filename: '' })

let pollTimer = null

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

async function upgrade(row) {
  try {
    await ElMessageBox.confirm(
      `确定把 ${row.name} 从 ${row.version || '未知版本'} 升级到 ${row.latest_version}？客户端会下载新版本、替换二进制并自动重启，期间其中转的代理会短暂中断。`,
      '在线升级',
      { type: 'warning' }
    )
  } catch {
    return
  }
  try {
    await api.post(`/api/clients/${row.id}/upgrade`)
    ElMessage.success('已下发升级指令')
    await load()
    startPolling()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

// 升级涉及下载与重启，短时间内轮询几次让状态自动收敛
function startPolling() {
  if (pollTimer) return
  let n = 0
  pollTimer = setInterval(async () => {
    n += 1
    await load()
    if (n >= 15) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }, 2000)
}

function onFileChange(file) {
  upload.file = file.raw
  upload.filename = file.name
}

async function openReleases() {
  releasesVisible.value = true
  await loadReleases()
}

async function loadReleases() {
  releaseLoading.value = true
  try {
    const { data } = await api.get('/api/releases')
    releases.value = data.items || []
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    releaseLoading.value = false
  }
}

async function submitRelease() {
  if (!upload.file) {
    ElMessage.warning('请选择要上传的客户端二进制文件')
    return
  }
  if (!upload.version) {
    ElMessage.warning('请填写版本号')
    return
  }
  const fd = new FormData()
  fd.append('file', upload.file)
  fd.append('version', upload.version)
  fd.append('os', upload.os)
  fd.append('arch', upload.arch)
  fd.append('note', upload.note)
  uploading.value = true
  try {
    await api.post('/api/releases', fd)
    ElMessage.success('上传成功')
    upload.file = null
    upload.filename = ''
    upload.version = ''
    await loadReleases()
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    uploading.value = false
  }
}

async function removeRelease(row) {
  try {
    await ElMessageBox.confirm(`确定删除 ${row.version} ${row.os}/${row.arch} 的升级包？`, '删除升级包', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.delete(`/api/releases/${row.id}`)
    ElMessage.success('已删除')
    await loadReleases()
    await load()
  } catch (e) {
    ElMessage.error(errText(e))
  }
}

function formatSize(n) {
  if (!n) return '-'
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${Math.round(n / 1024)} KB`
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
onUnmounted(() => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
})
</script>

<style scoped>
.code { background: #f5f7fa; padding: 12px; border-radius: 6px; white-space: pre-wrap; margin: 12px 0; }
.upload-row { display: flex; align-items: center; gap: 12px; margin-top: 16px; }
.upload-form { margin-top: 12px; }
</style>