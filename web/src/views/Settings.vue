<template>
  <div>
    <el-row :gutter="16">
      <el-col :span="12">
        <el-card shadow="never" header="运行信息">
          <el-descriptions :column="1" border>
            <el-descriptions-item label="版本">{{ info.version }}</el-descriptions-item>
            <el-descriptions-item label="主机名">{{ info.hostname }}</el-descriptions-item>
            <el-descriptions-item label="启动时间">{{ formatTime(info.started_at) }}</el-descriptions-item>
            <el-descriptions-item label="隧道地址">{{ info.tunnel_addr }}</el-descriptions-item>
            <el-descriptions-item label="管理地址">{{ info.admin_addr }}</el-descriptions-item>
            <el-descriptions-item label="代理端口范围">{{ portRange }}</el-descriptions-item>
            <el-descriptions-item label="空闲超时">{{ info.idle_timeout }}</el-descriptions-item>
            <el-descriptions-item label="本地登记自动审批">{{ info.auto_approve ? '是' : '否' }}</el-descriptions-item>
            <el-descriptions-item label="连接审计模式">{{ info.audit_mode }}</el-descriptions-item>
            <el-descriptions-item label="SQL 语句审计">
              {{ info.sql_log && info.sql_log.enabled ? `已启用（保留 ${info.sql_log.retention_days} 天，${info.sql_log.dir}）` : '未启用' }}
            </el-descriptions-item>
          </el-descriptions>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never" header="连接审计设置">
          <el-form label-width="130px">
            <el-form-item label="保留天数">
              <el-input-number v-model="retention" :min="1" :max="365" />
            </el-form-item>
            <el-form-item label="记录模式">
              <el-select v-model="mode" style="width: 180px">
                <el-option label="全量记录 (full)" value="full" />
                <el-option label="采样记录 (sample)" value="sample" />
                <el-option label="关闭 (off)" value="off" />
              </el-select>
            </el-form-item>
            <el-form-item>
              <el-button type="primary" :loading="saving" @click="saveSettings">保存</el-button>
              <span class="muted" style="margin-left: 10px">部分设置需重启服务端生效</span>
            </el-form-item>
          </el-form>
        </el-card>

        <el-card shadow="never" header="修改管理口令" style="margin-top: 16px">
          <el-form label-width="130px">
            <el-form-item label="原口令">
              <el-input v-model="pwd.old_password" type="password" show-password />
            </el-form-item>
            <el-form-item label="新口令">
              <el-input v-model="pwd.new_password" type="password" show-password placeholder="至少 8 位" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" :loading="changing" @click="changePassword">修改口令</el-button>
            </el-form-item>
          </el-form>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import api, { errText } from '../api'
import { formatTime } from '../utils'

const info = ref({})
const retention = ref(30)
const mode = ref('full')
const saving = ref(false)
const changing = ref(false)
const pwd = reactive({ old_password: '', new_password: '' })

const portRange = computed(() => (info.value.port_range ? `${info.value.port_range[0]} - ${info.value.port_range[1]}` : '-'))

async function load() {
  const [i, s] = await Promise.all([api.get('/api/system/info'), api.get('/api/settings')])
  info.value = i.data
  const rt = s.data.runtime || {}
  retention.value = rt.conn_log_retention || 30
  mode.value = rt.conn_log_mode || 'full'
}

async function saveSettings() {
  saving.value = true
  try {
    await api.put('/api/settings', {
      conn_log_retention_days: String(retention.value),
      conn_log_mode: mode.value
    })
    ElMessage.success('已保存（重启后生效）')
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    saving.value = false
  }
}

async function changePassword() {
  if (!pwd.old_password || pwd.new_password.length < 8) {
    ElMessage.warning('请填写原口令，新口令至少 8 位')
    return
  }
  changing.value = true
  try {
    await api.post('/api/auth/password', pwd)
    ElMessage.success('口令已修改，请重新登录')
    setTimeout(() => {
      location.href = '/login'
    }, 800)
  } catch (e) {
    ElMessage.error(errText(e))
  } finally {
    changing.value = false
  }
}

onMounted(load)
</script>