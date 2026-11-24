import axios from 'axios'

const api = axios.create({ baseURL: '/', timeout: 20000 })

function readCookie(name) {
  const m = document.cookie.match(new RegExp('(?:^|;\\s*)' + name + '=([^;]+)'))
  return m ? decodeURIComponent(m[1]) : ''
}

api.interceptors.request.use((cfg) => {
  const method = (cfg.method || 'get').toLowerCase()
  if (method !== 'get' && method !== 'head') {
    cfg.headers['X-CSRF-Token'] = readCookie('pt_csrf')
  }
  return cfg
})

api.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response && err.response.status === 401 && location.pathname !== '/login') {
      location.href = '/login'
    }
    return Promise.reject(err)
  }
)

// errText 统一提取后端错误信息
export function errText(err) {
  if (err && err.response && err.response.data && err.response.data.message) {
    return err.response.data.message
  }
  return (err && err.message) || '请求失败'
}

export default api