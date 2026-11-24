export function formatBytes(n) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = Number(n)
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatDuration(ms) {
  if (ms === null || ms === undefined || ms === 0) return '-'
  if (ms < 1000) return `${ms} ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分 ${s % 60} 秒`
  const h = Math.floor(m / 60)
  return `${h} 小时 ${m % 60} 分`
}

export function formatTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function closeReasonText(r) {
  const map = {
    eof: '正常关闭',
    timeout: '空闲超时',
    reset: '连接重置',
    rejected: '被拒绝',
    target_unreachable: '目标不可达',
    client_offline: '客户端离线',
    server_shutdown: '服务端关闭',
    unclean: '异常中断'
  }
  return map[r] || r || '-'
}

export function approvalText(a) {
  return { approved: '已通过', pending: '待审批', rejected: '已拒绝' }[a] || a
}