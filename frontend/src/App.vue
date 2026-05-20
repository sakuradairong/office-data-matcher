<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

// Wails 自动生成的绑定
import { OpenFileA, OpenFileB, ParseHeaders, RunMatch, RunMatchWithAI, ExportResults, SetAIConfig, SetAPIKey, GetAIStatus, ClearAICache, GetAICacheInfo } from '../wailsjs/go/main/App'
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'

// ----------- 文件与列映射 -----------
const fileAPath = ref('')
const fileBPath = ref('')
const headersA = ref([])
const headersB = ref([])

// 列映射索引（-1 表示未选/不使用）
const colAMatchIdx = ref(-1)
const colATimeIdx = ref(-1)
const colBMatchIdx = ref(-1)
const colBTimeIdx = ref(-1)
const colBExtractIdx = ref(-1)

// ----------- 高级匹配配置 -----------
const showAdvanced = ref(false)
const matchConfig = ref({
  regexPattern: '[^\\p{Han}]+',
  timeWindow: 12,
  threshold: 0.65,
  allMatches: false,
  caseSensitive: false,
  sortBy: '',
  maxPreview: 3,
  exportFormat: 'xlsx',
  includeHeader: true
})

// ----------- 状态 -----------
const loading = ref(false)
const results = ref([])
const exporting = ref(false)
const exportPath = ref('')
const errorMsg = ref('')
const stats = ref({ monthly: 0, daily: 0, matched: 0 })

// ----------- 进度状态 -----------
const progress = ref({ current: 0, total: 100, message: '', phase: '' })
const showProgress = ref(false)
const progressTimerId = ref(null)

// ----------- 进度辅助函数 -----------
function cancelProgressTimer() {
  if (progressTimerId.value !== null) {
    clearTimeout(progressTimerId.value)
    progressTimerId.value = null
  }
}

function scheduleProgressDone() {
  cancelProgressTimer()
  progressTimerId.value = setTimeout(() => {
    showProgress.value = false
    progressTimerId.value = null
  }, 1500)
}

function hideProgressNow() {
  cancelProgressTimer()
  showProgress.value = false
}

// ----------- AI API 配置 -----------
const apiKey = ref('')
const apiEndpoint = ref('')
const apiModel = ref('')
const aiReady = ref(false)
const showApiInput = ref(false)
const aiEnhancing = ref(false)

// ----------- AI 缓存状态 -----------
const cacheInfo = ref({ count: 0, filePath: '' })

async function refreshCacheInfo() {
  try { cacheInfo.value = await GetAICacheInfo() } catch { /* ignore */ }
}

async function clearCache() {
  try {
    const msg = await ClearAICache()
    await refreshCacheInfo()
    errorMsg.value = ''
  } catch (e) {
    errorMsg.value = '清除缓存失败: ' + (e.message || e)
  }
}

// ----------- 文件选择 -----------
async function selectFileA() {
  errorMsg.value = ''
  const path = await OpenFileA()
  if (!path) return
  fileAPath.value = path
  // 读表头用于动态下拉框
  try {
    headersA.value = await ParseHeaders(path)
    colAMatchIdx.value = -1; colATimeIdx.value = -1
  } catch (e) { errorMsg.value = '读取 A 表头失败: ' + (e.message || e) }
}

async function selectFileB() {
  errorMsg.value = ''
  const path = await OpenFileB()
  if (!path) return
  fileBPath.value = path
  try {
    headersB.value = await ParseHeaders(path)
    colBMatchIdx.value = -1; colBTimeIdx.value = -1; colBExtractIdx.value = -1
  } catch (e) { errorMsg.value = '读取 B 表头失败: ' + (e.message || e) }
}

// ----------- 智能匹配 -----------
async function startMatching() {
  if (loading.value) return // 防止重复点击
  if (!fileAPath.value || !fileBPath.value) return
  if (colAMatchIdx.value < 0 || colBMatchIdx.value < 0 || colBExtractIdx.value < 0) {
    errorMsg.value = '请完成列映射配置（A表匹配列 / B表匹配列 / B表提取列）'
    return
  }
  cancelProgressTimer()
  loading.value = true; aiEnhancing.value = false; showProgress.value = true
  errorMsg.value = ''; results.value = []; exportPath.value = ''
  progress.value = { current: 0, total: 100, message: '准备中...', phase: 'reading' }

  try {
    const data = await RunMatch(buildMatchConfig())
    results.value = data; stats.value.matched = data.length
  } catch (err) { errorMsg.value = typeof err === 'string' ? err : (err.message || '匹配失败')
    hideProgressNow()
  } finally { loading.value = false; if (!errorMsg.value) scheduleProgressDone() }
}

// buildMatchConfig 从响应式状态构建 MatchConfig 对象（消除重复）
function buildMatchConfig() {
  return {
    fileAPath: fileAPath.value, fileBPath: fileBPath.value,
    colAMatchIndex: colAMatchIdx.value, colATimeIndex: colATimeIdx.value,
    colBMatchIndex: colBMatchIdx.value, colBTimeIndex: colBTimeIdx.value,
    colBExtractIndex: colBExtractIdx.value,
    regexPattern: matchConfig.value.regexPattern || '',
    timeWindow: Number(matchConfig.value.timeWindow) || 12,
    threshold: Number(matchConfig.value.threshold) || 0.65,
    allMatches: matchConfig.value.allMatches || false,
    caseSensitive: matchConfig.value.caseSensitive || false,
    sortBy: matchConfig.value.sortBy || '',
    maxPreview: Number(matchConfig.value.maxPreview) || 0,
    exportFormat: matchConfig.value.exportFormat || 'xlsx',
    includeHeader: matchConfig.value.includeHeader !== false
  }
}

// ----------- AI 增强匹配 -----------
async function startAIEnhance() {
  if (loading.value) return // 防止重复点击
  if (!fileAPath.value || !fileBPath.value) {
    errorMsg.value = '请先选择 A 表和 B 表文件'
    return
  }
  if (colAMatchIdx.value < 0 || colBMatchIdx.value < 0 || colBExtractIdx.value < 0) {
    errorMsg.value = '请完成列映射配置（A表匹配列 / B表匹配列 / B表提取列）'
    return
  }
  if (!aiReady.value && !apiKey.value) {
    errorMsg.value = '请先配置 AI API 密钥'
    return
  }

  cancelProgressTimer()

  if (!aiReady.value && apiKey.value) {
    await SetAIConfig(apiEndpoint.value, apiModel.value, apiKey.value)
    aiReady.value = true
  }

  aiEnhancing.value = true
  loading.value = true
  showProgress.value = true
  errorMsg.value = ''
  results.value = []
  exportPath.value = ''
  progress.value = { current: 0, total: 100, message: '正在启动 AI 增强匹配...', phase: 'reading' }

  try {
    const data = await RunMatchWithAI(buildMatchConfig())
    results.value = data
    stats.value.matched = data.length
  } catch (err) {
    errorMsg.value = typeof err === 'string' ? err : (err.message || 'AI 增强匹配失败')
    hideProgressNow()
  } finally {
    loading.value = false
    aiEnhancing.value = false
    if (!errorMsg.value) {
      scheduleProgressDone()
    }
  }
}

// ----------- 导出 -----------
async function exportResult() {
  if (results.value.length === 0) return
  exporting.value = true
  try {
    const path = await ExportResults(results.value)
    if (path) exportPath.value = path
  } catch (err) {
    errorMsg.value = typeof err === 'string' ? err : (err.message || '导出失败')
  } finally {
    exporting.value = false
  }
}

// ----------- AI API 密钥管理 -----------
async function saveApiConfig() {
  if (!apiKey.value) return
  await SetAIConfig(apiEndpoint.value, apiModel.value, apiKey.value)
  const status = await GetAIStatus()
  aiReady.value = status.ready === 'true'
  if (aiReady.value) {
    setTimeout(() => { showApiInput.value = false }, 1000)
  }
}

// ----------- 进度监听 -----------

onMounted(async () => {
  // 恢复 AI API 配置
  try {
    const status = await GetAIStatus()
    aiReady.value = status.ready === 'true'
    apiEndpoint.value = status.endpoint || ''
    apiModel.value = status.model || ''
  } catch { /* ignore */ }
  // 检查 AI 缓存状态
  await refreshCacheInfo()
  EventsOn('match-progress', (data) => {
    progress.value = {
      current: data.current,
      total: data.total,
      message: data.message,
      phase: data.phase
    }
  })
})

onUnmounted(() => {
  cancelProgressTimer()
  EventsOff('match-progress')
})

// ----------- 计算属性 -----------
const canMatch = computed(() => fileAPath.value && fileBPath.value && !loading.value)
const hasResults = computed(() => results.value.length > 0)
const progressPercent = computed(() => {
  const p = progress.value
  if (p.total === 0) return 0
  return Math.round((p.current / p.total) * 100)
})
const aiMatchedCount = computed(() => results.value.filter(r => r.aiMatched).length)
const basicMatchedCount = computed(() => results.value.length - aiMatchedCount.value)

// ----------- 辅助函数 -----------
function scoreClass(score) {
  if (score >= 0.9) return 'score-high'
  if (score >= 0.75) return 'score-mid'
  return 'score-low'
}
</script>

<template>
  <div class="app-container">
    <!-- 顶部标题 -->
    <header class="app-header">
      <div class="header-icon">
        <svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
          <polyline points="7 10 12 15 17 10"/>
          <line x1="12" y1="15" x2="12" y2="3"/>
        </svg>
      </div>
      <div class="header-text">
        <h1>数据智能匹配工具</h1>
        <p class="subtitle">日报中断原因 → 月报精准匹配</p>
      </div>
    </header>

    <!-- 操作面板 -->
    <section class="panel operation-panel">
      <div class="file-selectors">
        <!-- A 表 -->
        <div class="file-row">
          <label class="file-label"><span class="label-icon">📅</span>A 表（基准表）</label>
          <div class="file-input-group">
            <button class="btn btn-outline" @click="selectFileA" :disabled="loading">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>选择文件
            </button>
            <span class="file-path" :class="{ selected: fileAPath }">{{ fileAPath || '尚未选择' }}</span>
          </div>
        </div>
        <div v-if="headersA.length" class="mapping-row">
          <label class="mapping-label">匹配列</label>
          <select v-model.number="colAMatchIdx" class="col-select"><option :value="-1">-- 请选择 --</option><option v-for="(h,i) in headersA" :key="i" :value="i">{{ h }}</option></select>
          <label class="mapping-label">时间列</label>
          <select v-model.number="colATimeIdx" class="col-select"><option :value="-1">跳过时间</option><option v-for="(h,i) in headersA" :key="i" :value="i">{{ h }}</option></select>
        </div>
        <!-- B 表 -->
        <div class="file-row">
          <label class="file-label"><span class="label-icon">📋</span>B 表（数据源表）</label>
          <div class="file-input-group">
            <button class="btn btn-outline" @click="selectFileB" :disabled="loading">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>选择文件
            </button>
            <span class="file-path" :class="{ selected: fileBPath }">{{ fileBPath || '尚未选择' }}</span>
          </div>
        </div>
        <div v-if="headersB.length" class="mapping-row">
          <label class="mapping-label">匹配列</label>
          <select v-model.number="colBMatchIdx" class="col-select"><option :value="-1">-- 请选择 --</option><option v-for="(h,i) in headersB" :key="i" :value="i">{{ h }}</option></select>
          <label class="mapping-label">时间列</label>
          <select v-model.number="colBTimeIdx" class="col-select"><option :value="-1">跳过时间</option><option v-for="(h,i) in headersB" :key="i" :value="i">{{ h }}</option></select>
          <label class="mapping-label">提取列</label>
          <select v-model.number="colBExtractIdx" class="col-select"><option :value="-1">-- 请选择 --</option><option v-for="(h,i) in headersB" :key="i" :value="i">{{ h }}</option></select>
        </div>
      </div>

      <div class="action-row action-row--multi">
        <button
          class="btn btn-primary btn-large"
          :disabled="!canMatch"
          @click="startMatching"
        >
          <template v-if="loading && !aiEnhancing">
            <span class="spinner"></span>
            匹配中...
          </template>
          <template v-else>
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>
            </svg>
            开始智能匹配
          </template>
        </button>
        <button
          class="btn btn-ai"
          :disabled="!canMatch || !aiReady || loading"
          @click="startAIEnhance"
        >
          <template v-if="aiEnhancing">
            <span class="spinner spinner-dark"></span>
            AI 增强中...
          </template>
          <template v-else>
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M12 2a4 4 0 0 1 4 4c0 2-2 4-4 4s-4-2-4-4a4 4 0 0 1 4-4z"/>
              <path d="M2 22c0-4 4-8 10-8s10 4 10 8"/>
            </svg>
            AI 增强匹配
          </template>
        </button>
      </div>

      <!-- 高级设置 -->
      <div class="advanced-config">
        <button class="btn btn-text" @click="showAdvanced = !showAdvanced">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="12" cy="12" r="3"/>
            <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/>
          </svg>
          {{ showAdvanced ? '收起匹配规则' : '匹配规则设置' }}
        </button>
        <transition name="slide">
          <div v-if="showAdvanced" class="advanced-form">
            <div class="form-row">
              <label class="form-label">
                清洗正则
                <span class="form-hint">匹配内容将被剔除，默认保留纯中文</span>
              </label>
              <input
                type="text"
                v-model="matchConfig.regexPattern"
                class="form-input mono"
                placeholder="[^\p{Han}]+"
                :disabled="loading"
              />
            </div>
            <div class="form-row">
              <label class="form-label">
                时间容错窗口
                <span class="form-hint">单位：小时</span>
              </label>
              <input
                type="number"
                v-model.number="matchConfig.timeWindow"
                class="form-input narrow"
                min="0"
                max="168"
                step="1"
                :disabled="loading"
              />
            </div>
            <div class="form-row">
              <label class="form-label">
                相似度阈值
                <span class="form-hint">当前值：{{ matchConfig.threshold.toFixed(2) }}</span>
              </label>
              <input
                type="range"
                v-model.number="matchConfig.threshold"
                class="form-slider"
                min="0"
                max="1"
                step="0.05"
                :disabled="loading"
              />
              <div class="slider-labels">
                <span>0</span>
                <span class="slider-tick" v-for="v in [0.25,0.5,0.65,0.8,0.95]" :key="v" :style="{ left: (v*100)+'%' }">|</span>
                <span>1</span>
              </div>
            </div>
            <div class="form-row form-row--cols">
              <label class="form-label">
                匹配策略
                <span class="form-hint">全部返回 vs 仅最佳</span>
              </label>
              <label class="toggle-label">
                <input type="checkbox" v-model="matchConfig.allMatches" class="toggle-input" :disabled="loading" />
                <span class="toggle-text">{{ matchConfig.allMatches ? '全部匹配' : '仅最佳匹配' }}</span>
              </label>
              <label class="form-label" style="margin-left:20px">
                调试预览
              </label>
              <input type="number" v-model.number="matchConfig.maxPreview" class="form-input narrow" min="0" max="50" step="1" :disabled="loading" />
            </div>
            <div class="form-row form-row--cols">
              <label class="form-label">
                结果排序
                <span class="form-hint">匹配结果排序方式</span>
              </label>
              <select v-model="matchConfig.sortBy" class="col-select" style="max-width:160px" :disabled="loading">
                <option value="">不排序</option>
                <option value="similarity">相似度降序</option>
                <option value="timeDiff">时间差升序</option>
              </select>
              <label class="form-label" style="margin-left:20px">
                导出格式
              </label>
              <select v-model="matchConfig.exportFormat" class="col-select" style="max-width:100px" :disabled="loading">
                <option value="xlsx">Excel</option>
                <option value="csv">CSV</option>
              </select>
            </div>
          </div>
        </transition>
      </div>

      <!-- AI API 配置 -->
      <div class="ai-config">
        <button class="btn btn-text" @click="showApiInput = !showApiInput">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="12" cy="12" r="3"/>
            <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09a1.65 1.65 0 0 0-1.08-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1.08 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1.08z"/>
          </svg>
          {{ showApiInput ? '收起 API 配置' : '配置 AI API' }}
          <span class="status-dot" :class="{ active: aiReady }"></span>
        </button>
        <transition name="slide">
          <div v-if="showApiInput" class="api-input-row">
            <input
              type="text"
              v-model="apiEndpoint"
              placeholder="API 端点 (如 http://localhost:8080)"
              class="api-input api-endpoint"
              :disabled="loading"
            />
            <input
              type="text"
              v-model="apiModel"
              placeholder="模型 (默认 deepseek-chat)"
              class="api-input api-model"
              :disabled="loading"
            />
            <input
              type="password"
              v-model="apiKey"
              placeholder="API 密钥 (sk-...)"
              class="api-input"
              :disabled="loading"
            />
            <button
              class="btn btn-sm btn-outline"
              @click="saveApiConfig"
              :disabled="!apiKey || loading"
            >
              保存
            </button>
            <span v-if="aiReady" class="api-status ok">已配置</span>
            <span v-else class="api-status na">未配置</span>
          </div>
        </transition>
      </div>

      <!-- AI 缓存管理 -->
      <div class="cache-config" v-if="cacheInfo.count >= 0">
        <button class="btn btn-text" @click="refreshCacheInfo">
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/>
            <path d="M21 3v5h-5"/>
          </svg>
          AI 缓存：{{ cacheInfo.count }} 条
        </button>
        <button
          v-if="cacheInfo.count > 0"
          class="btn btn-sm btn-outline btn-danger-outline"
          @click="clearCache"
          :disabled="loading"
          style="margin-left: 8px"
        >
          清除缓存
        </button>
      </div>

      <!-- 错误提示 -->
      <transition name="fade">
        <div v-if="errorMsg" class="error-banner">
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="12" cy="12" r="10"/>
            <line x1="12" y1="8" x2="12" y2="12"/>
            <line x1="12" y1="16" x2="12.01" y2="16"/>
          </svg>
          <span>{{ errorMsg }}</span>
          <button class="error-close" @click="errorMsg = ''">&times;</button>
        </div>
      </transition>
    </section>

    <!-- 进度面板（放在操作面板与结果之间，避免被旧定时器误关） -->
    <section class="panel progress-panel" v-if="showProgress">
      <div class="progress-state">
        <div class="progress-header">
          <span class="progress-phase">
            <span v-if="progress.phase === 'reading'" class="phase-icon">📂</span>
            <span v-else-if="progress.phase === 'matching'" class="phase-icon">🔗</span>
            <span v-else-if="progress.phase === 'ai-enhancing'" class="phase-icon">🤖</span>
            <span v-else class="phase-icon">✅</span>
            {{ progress.message }}
          </span>
          <span class="progress-pct">{{ progressPercent }}%</span>
        </div>
        <div class="progress-bar-track">
          <div
            class="progress-bar-fill"
            :class="{
              'fill-matching': progress.phase === 'matching',
              'fill-ai': progress.phase === 'ai-enhancing',
              'fill-done': progress.phase === 'done'
            }"
            :style="{ width: progressPercent + '%' }"
          ></div>
        </div>
        <div class="progress-sub">
          <span v-if="progress.phase === 'matching'">
            已处理 {{ progress.current }} / {{ progress.total }} 条
          </span>
          <span v-else-if="progress.phase === 'ai-enhancing'">
            AI 分析中 {{ progress.current }} / {{ progress.total }}
          </span>
          <span v-else>&nbsp;</span>
        </div>
      </div>
    </section>

    <!-- 结果面板 -->
    <section class="panel result-panel" v-if="hasResults">
      <div class="result-header">
        <div class="result-title">
          <h2>
            匹配结果
            <span class="badge">{{ results.length }} 条</span>
            <span v-if="aiMatchedCount > 0" class="badge badge-ai">AI 辅助 {{ aiMatchedCount }} 条</span>
            <span v-else class="badge badge-basic">基础匹配 {{ basicMatchedCount }} 条</span>
          </h2>
        </div>
        <div class="result-actions">
          <button
            class="btn btn-success"
            @click="exportResult"
            :disabled="exporting"
          >
            <template v-if="exporting">
              <span class="spinner"></span>
              导出中...
            </template>
            <template v-else>
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                <polyline points="7 10 12 15 17 10"/>
                <line x1="12" y1="15" x2="12" y2="3"/>
              </svg>
              导出结果
            </template>
          </button>
        </div>
      </div>

      <!-- 导出路径提示 -->
      <transition name="fade">
        <div v-if="exportPath" class="success-banner">
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
            <polyline points="22 4 12 14.01 9 11.01"/>
          </svg>
          <span>导出成功：{{ exportPath }}</span>
        </div>
      </transition>

      <!-- 数据表格（新通用格式） -->
      <div class="table-wrapper">
        <table class="result-table">
          <thead><tr>
            <th v-for="(h,i) in headersA" :key="'ha'+i">{{ h || ('Col'+(i+1)) }}</th>
            <th class="col-extract">匹配结果(由B表提取)</th>
          </tr></thead>
          <tbody>
            <tr v-for="(r, idx) in results" :key="idx">
              <td v-for="(h,i) in headersA" :key="'da'+i" :title="r.rowAData?.[i]">{{ r.rowAData?.[i] }}</td>
              <td class="col-extract" :title="r.extractValue">{{ r.extractValue }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- 空状态 -->
    <section class="panel empty-panel" v-else-if="!loading && !errorMsg">
      <div class="empty-state">
        <svg viewBox="0 0 24 24" width="64" height="64" fill="none" stroke="currentColor" stroke-width="1" class="empty-icon">
          <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/>
          <polyline points="14 2 14 8 20 8"/>
          <line x1="16" y1="13" x2="8" y2="13"/>
          <line x1="16" y1="17" x2="8" y2="17"/>
          <polyline points="10 9 9 9 8 9"/>
        </svg>
        <h3>等待匹配</h3>
        <p>请先选择月报和日报文件，然后点击「开始智能匹配」</p>
      </div>
    </section>

  </div>
</template>

<style scoped>
/* ===== 暗色主题全局 ===== */
.app-container {
  max-width: 1280px;
  margin: 0 auto;
  padding: 32px 40px 64px;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', 'Noto Sans SC', sans-serif;
}

/* ===== 头部 ===== */
.app-header {
  display: flex;
  align-items: center;
  gap: 20px;
  margin-bottom: 36px;
}
.header-icon {
  display: flex; align-items: center; justify-content: center;
  width: 60px; height: 60px;
  background: linear-gradient(135deg, #667eea, #764ba2);
  border-radius: 18px; color: white; flex-shrink: 0;
  box-shadow: 0 8px 32px rgba(102, 126, 234, 0.35);
}
.header-text h1 {
  font-size: 26px; font-weight: 800; color: #fff; margin: 0 0 4px;
  letter-spacing: -0.5px;
}
.subtitle {
  font-size: 14px; color: rgba(255,255,255,0.45); margin: 0; font-weight: 400;
}

/* ===== 卡片面板 ===== */
.panel {
  background: rgba(255,255,255,0.04);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(255,255,255,0.08);
  border-radius: 20px;
  margin-bottom: 24px;
  padding: 28px;
  box-shadow: 0 4px 24px rgba(0,0,0,0.2);
}

/* ===== 文件选择器 ===== */
.file-selectors { display: flex; flex-direction: column; gap: 16px; margin-bottom: 8px; }
.file-row { display: flex; align-items: center; gap: 14px; }
.file-label {
  display: flex; align-items: center; gap: 8px;
  min-width: 110px; font-weight: 700; font-size: 14px; color: rgba(255,255,255,0.8);
}
.label-icon { font-size: 18px; }
.file-input-group { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 0; }
.file-path {
  font-size: 13px; color: rgba(255,255,255,0.35);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1;
}
.file-path.selected { color: rgba(255,255,255,0.7); font-weight: 500; }

/* ===== 列映射下拉框 ===== */
.mapping-row {
  display: flex; align-items: center; gap: 10px; margin-top: 2px;
  margin-left: 124px; flex-wrap: wrap;
}
.mapping-label {
  font-size: 12px; font-weight: 600; color: rgba(255,255,255,0.5);
  min-width: 52px; text-transform: uppercase; letter-spacing: 0.5px;
}
.col-select {
  flex: 1; min-width: 130px; max-width: 220px;
  padding: 8px 12px; font-size: 12px;
  border: 1px solid rgba(255,255,255,0.1);
  border-radius: 10px; outline: none;
  background: rgba(255,255,255,0.05); color: rgba(255,255,255,0.8);
  transition: all 0.2s;
}
.col-select:focus {
  border-color: #667eea;
  box-shadow: 0 0 0 3px rgba(102,126,234,0.15);
}
.col-select option { background: #1a1a2e; color: #fff; }

/* ===== 按钮 ===== */
.btn {
  display: inline-flex; align-items: center; gap: 8px;
  padding: 10px 20px; font-size: 14px; font-weight: 600;
  border: 1px solid transparent; border-radius: 12px;
  cursor: pointer; transition: all 0.25s ease; white-space: nowrap;
}
.btn:disabled { opacity: 0.35; cursor: not-allowed; }
.btn-outline {
  background: rgba(255,255,255,0.06);
  border-color: rgba(255,255,255,0.12);
  color: rgba(255,255,255,0.8);
}
.btn-outline:hover:not(:disabled) {
  background: rgba(255,255,255,0.1);
  border-color: rgba(255,255,255,0.25);
}
.btn-primary {
  background: linear-gradient(135deg, #667eea, #764ba2);
  color: white; border: none; padding: 14px 36px; font-size: 16px;
  font-weight: 700; border-radius: 14px;
  box-shadow: 0 8px 32px rgba(102, 126, 234, 0.3);
}
.btn-primary:hover:not(:disabled) {
  transform: translateY(-2px);
  box-shadow: 0 12px 40px rgba(102, 126, 234, 0.45);
}
.btn-primary:active:not(:disabled) { transform: translateY(0); }
.btn-success {
  background: linear-gradient(135deg, #00b894, #00cec9);
  color: white; border: none; padding: 10px 24px; font-weight: 600;
  border-radius: 12px; box-shadow: 0 4px 16px rgba(0,184,148,0.25);
}
.btn-success:hover:not(:disabled) {
  background: linear-gradient(135deg, #00a381, #00b5b0);
  transform: translateY(-1px);
}
.btn-ai {
  background: linear-gradient(135deg, #a855f7, #6366f1);
  color: white; border: none; padding: 14px 30px; font-weight: 700;
  border-radius: 14px; font-size: 15px;
  box-shadow: 0 8px 28px rgba(168,85,247,0.3);
}
.btn-ai:hover:not(:disabled) {
  transform: translateY(-2px);
  box-shadow: 0 12px 36px rgba(168,85,247,0.45);
}
.btn-ai:active:not(:disabled) { transform: translateY(0); }
.btn-ai:disabled { opacity: 0.35; cursor: not-allowed; }
.btn-text {
  background: none; border: none; color: rgba(255,255,255,0.45);
  font-size: 13px; padding: 6px 10px; border-radius: 8px;
  cursor: pointer; display: inline-flex; align-items: center; gap: 6px;
}
.btn-text:hover { color: #667eea; background: rgba(102,126,234,0.08); }
.btn-sm { padding: 7px 16px; font-size: 13px; }
.btn-large { min-width: 200px; justify-content: center; }
.action-row { display: flex; justify-content: center; }
.action-row--multi { gap: 14px; flex-wrap: wrap; }

/* ===== Spinner ===== */
.spinner {
  display: inline-block; width: 18px; height: 18px;
  border: 2.5px solid rgba(255,255,255,0.25);
  border-top-color: white; border-radius: 50%;
  animation: spin 0.7s linear infinite;
}
.spinner-dark { border-color: rgba(255,255,255,0.2); border-top-color: white; }
@keyframes spin { to { transform: rotate(360deg); } }

/* ===== 横幅 ===== */
.error-banner, .success-banner {
  display: flex; align-items: center; gap: 12px;
  padding: 14px 18px; border-radius: 14px; margin-top: 18px; font-size: 14px;
  animation: slideIn 0.3s ease;
}
@keyframes slideIn { from { opacity: 0; transform: translateY(-8px); } to { opacity: 1; transform: translateY(0); } }
.error-banner {
  background: rgba(220,38,38,0.12); color: #fca5a5;
  border: 1px solid rgba(220,38,38,0.2);
}
.success-banner {
  background: rgba(16,185,129,0.12); color: #6ee7b7;
  border: 1px solid rgba(16,185,129,0.2); margin-bottom: 16px;
}
.error-close { margin-left: auto; background: none; border: none; font-size: 22px; cursor: pointer; color: inherit; opacity: 0.5; }
.error-close:hover { opacity: 1; }

/* ===== 结果面板 ===== */
.result-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.result-title h2 {
  font-size: 20px; font-weight: 700; color: #fff; margin: 0;
  display: flex; align-items: center; gap: 12px;
}
.badge {
  display: inline-flex; align-items: center; padding: 3px 12px;
  font-size: 12px; font-weight: 700; border-radius: 20px;
  color: #667eea; background: rgba(102,126,234,0.15);
}
.badge-ai { color: #a855f7; background: rgba(168,85,247,0.15); }

/* ===== 表格 ===== */
.table-wrapper {
  overflow-x: auto; border-radius: 14px;
  border: 1px solid rgba(255,255,255,0.06);
}
.result-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.result-table thead { background: rgba(255,255,255,0.03); }
.result-table th {
  padding: 14px 16px; text-align: left;
  font-weight: 700; color: rgba(255,255,255,0.6); font-size: 11px;
  text-transform: uppercase; letter-spacing: 0.5px;
  border-bottom: 1px solid rgba(255,255,255,0.06); white-space: nowrap;
}
.result-table td {
  padding: 12px 16px; color: rgba(255,255,255,0.75);
  border-bottom: 1px solid rgba(255,255,255,0.03);
  max-width: 240px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 13px;
}
.result-table tbody tr { transition: background 0.15s; }
.result-table tbody tr:hover { background: rgba(102,126,234,0.08); }
.result-table tbody tr:last-child td { border-bottom: none; }
.col-extract {
  min-width: 180px; font-weight: 600;
  color: #a78bfa; background: rgba(167,139,250,0.06);
}

/* ===== 空状态 ===== */
.empty-state { text-align: center; padding: 60px 20px; }
.empty-icon { color: rgba(255,255,255,0.1); margin-bottom: 20px; }
.empty-state h3 { font-size: 20px; font-weight: 700; color: rgba(255,255,255,0.4); margin: 0 0 10px; }
.empty-state p { font-size: 14px; color: rgba(255,255,255,0.25); margin: 0; }

/* ===== 进度条 ===== */
.progress-panel { background: rgba(255,255,255,0.05); }
.progress-state { padding: 4px 0; }
.progress-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 14px; }
.progress-phase { font-size: 14px; font-weight: 600; color: rgba(255,255,255,0.75); display: flex; align-items: center; gap: 8px; }
.phase-icon { font-size: 18px; }
.progress-pct { font-size: 22px; font-weight: 800; color: #667eea; }
.progress-bar-track {
  width: 100%; height: 10px; background: rgba(255,255,255,0.06);
  border-radius: 5px; overflow: hidden;
}
.progress-bar-fill {
  height: 100%; border-radius: 5px; transition: width 0.4s ease;
  background: linear-gradient(90deg, #667eea, #764ba2);
  box-shadow: 0 0 12px rgba(102,126,234,0.3);
}
.fill-matching { background: linear-gradient(90deg, #667eea, #764ba2); }
.fill-ai { background: linear-gradient(90deg, #a855f7, #6366f1); }
.fill-done { background: linear-gradient(90deg, #00b894, #00cec9); }
.progress-sub { margin-top: 10px; font-size: 12px; color: rgba(255,255,255,0.3); min-height: 20px; }

/* ===== AI API 配置 ===== */
.ai-config { margin-top: 18px; padding-top: 16px; border-top: 1px solid rgba(255,255,255,0.06); }
.cache-config {
  margin-top: 12px; display: flex; align-items: center; gap: 6px;
}
.btn-danger-outline {
  border-color: rgba(220,38,38,0.25); color: #fca5a5;
}
.btn-danger-outline:hover:not(:disabled) {
  background: rgba(220,38,38,0.1);
  border-color: rgba(220,38,38,0.4);
}
.status-dot {
  display: inline-block; width: 8px; height: 8px; border-radius: 50%;
  background: rgba(255,255,255,0.2);
}
.status-dot.active {
  background: #00b894; box-shadow: 0 0 10px rgba(0,184,148,0.5);
}
.api-input-row { display: flex; align-items: center; gap: 10px; margin-top: 12px; }
.api-input {
  flex: 1; max-width: 440px; padding: 10px 14px; font-size: 13px;
  border: 1px solid rgba(255,255,255,0.1); border-radius: 10px; outline: none;
  background: rgba(255,255,255,0.05); color: rgba(255,255,255,0.8);
  font-family: 'SF Mono', 'Fira Code', monospace;
  transition: border-color 0.2s;
}
.api-input:focus { border-color: #667eea; box-shadow: 0 0 0 3px rgba(102,126,234,0.12); }
.api-endpoint,
.api-model {
  max-width: 320px;
  font-size: 13px;
}
.api-status { font-size: 12px; font-weight: 700; padding: 3px 10px; border-radius: 6px; }
.api-status.ok { color: #6ee7b7; background: rgba(16,185,129,0.12); }
.api-status.na { color: #fcd34d; background: rgba(251,191,36,0.1); }

/* ===== 高级设置 ===== */
.advanced-config { margin-top: 18px; padding-top: 16px; border-top: 1px solid rgba(255,255,255,0.06); }
.advanced-form {
  display: flex; flex-direction: column; gap: 14px; margin-top: 14px;
  padding: 20px; background: rgba(255,255,255,0.03);
  border-radius: 14px; border: 1px solid rgba(255,255,255,0.06);
}
.form-row { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.form-label {
  min-width: 110px; font-size: 13px; font-weight: 600;
  color: rgba(255,255,255,0.65); display: flex; flex-direction: column; gap: 3px;
}
.form-hint { font-weight: 400; font-size: 11px; color: rgba(255,255,255,0.3); }
.form-input {
  flex: 1; padding: 9px 14px; font-size: 13px;
  border: 1px solid rgba(255,255,255,0.1); border-radius: 10px; outline: none;
  background: rgba(255,255,255,0.05); color: rgba(255,255,255,0.8);
  transition: border-color 0.2s;
}
.form-input:focus { border-color: #667eea; box-shadow: 0 0 0 3px rgba(102,126,234,0.12); }
.form-input.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.form-input.narrow { max-width: 130px; }
.form-slider { flex: 1; max-width: 300px; height: 6px; accent-color: #667eea; }

/* ===== 多列表单行 ===== */
.form-row--cols { flex-wrap: nowrap; }
.toggle-label { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.toggle-input {
  width: 40px; height: 22px; appearance: none;
  background: rgba(255,255,255,0.1); border: 1px solid rgba(255,255,255,0.15);
  border-radius: 11px; position: relative; cursor: pointer; outline: none;
  transition: background 0.25s;
}
.toggle-input::after {
  content: ''; position: absolute; top: 2px; left: 2px;
  width: 16px; height: 16px; border-radius: 50%;
  background: rgba(255,255,255,0.5); transition: transform 0.25s;
}
.toggle-input:checked { background: #667eea; border-color: #667eea; }
.toggle-input:checked::after { transform: translateX(18px); background: white; }
.toggle-text { font-size: 12px; color: rgba(255,255,255,0.55); }

/* ===== 过渡动画 ===== */
.fade-enter-active, .fade-leave-active { transition: opacity 0.3s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
.slide-enter-active, .slide-leave-active { transition: all 0.25s ease; overflow: hidden; }
.slide-enter-from, .slide-leave-to { max-height: 0; opacity: 0; }
.slide-enter-to, .slide-leave-from { max-height: 400px; opacity: 1; }
</style>
