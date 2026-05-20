# 数据智能匹配工具 (Data Matcher)

基于 **Wails v2** (Go + Vue 3) 的桌面端数据匹配工具，支持 Excel/CSV 文件的智能列映射、模糊匹配与 AI 增强匹配。

## 功能特性

- 📂 **多格式支持** — 读取 `.xlsx` / `.xls` / `.csv`
- 🔗 **动态列映射** — 自动解析表头，前端下拉框配置匹配列、时间列、提取列
- 🧹 **正则清洗** — 自定义正则剔除干扰字符（默认保留纯中文）
- ⏱ **时间窗口剪枝** — 按时间差过滤候选记录，减少无效比对
- 📊 **Levenshtein 模糊匹配** — 基于编辑距离的相似度计算，支持大小写敏感、全量/最佳匹配、结果排序
- 🤖 **AI 增强匹配** — 对基础匹配未命中的记录调用大模型二次匹配，支持行级缓存跨批次复用
- 🗄 **AI 缓存** — 批量 prompt 缓存 + 单行结果缓存，持久化到临时文件，命中后零 API 消耗
- 🌐 **兼容 OpenAI 格式** — 支持 Deepseek、OpenAI、Ollama、vLLM 等任何兼容 `/v1/chat/completions` 的 API
- 📤 **多格式导出** — 导出为 Excel (`.xlsx`) 或 CSV (`.csv`)，可选包含表头行

## 技术栈

| 层级     | 技术                                  |
| -------- | ------------------------------------- |
| 后端     | Go 1.24 + Wails v2.12.0               |
| 前端     | Vue 3 (Composition API) + Vite         |
| Excel    | excelize v2.10.1                       |
| AI API   | OpenAI 兼容格式（Deepseek / OpenAI / Ollama / 自定义） |

## 快速开始

### 前置要求

- Go 1.24+
- Node.js 18+
- Wails CLI v2.12.0

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

### 开发模式

```bash
cd frontend && npm install
wails dev
```

开发模式下 Vite 热重载运行在 `http://localhost:5173`。

### 构建

```bash
# 日常构建（17s，~4.8 MB）
wails build -ldflags="-s -w" -trimpath -upx

# 发行构建（60s，~4.0 MB）
wails build -ldflags="-s -w" -trimpath -upx -upxflags="--ultra-brute"
```

> 需要 [UPX](https://upx.github.io/) 用于二进制压缩，可通过 `winget install UPX.UPX` 安装。

## 使用指南

### 基础匹配
1. 点击「选择文件」分别加载 A 表（基准表）和 B 表（数据源表）
2. 表头自动识别后，在下拉框中配置列映射：
   - **匹配列** — 用于模糊匹配的文本列
   - **时间列** — 可选，用于时间窗口剪枝
   - **提取列** — 从 B 表提取到结果的目标列
3. 点击「开始智能匹配」运行基础算法
4. 可选切换高级匹配规则（相似度阈值、时间窗口、排序、全量/最佳匹配等）
5. 点击「导出结果」保存为 Excel 或 CSV

### AI 增强匹配
1. 展开底部「配置 AI API」，填入：
   - **端点** — API 地址，只需填 base URL，如 `http://localhost:8080`（自动补齐 `/v1/chat/completions`）
   - **模型** — 模型名称，如 `gpt-4o` / `deepseek-chat` / `llama3`
   - **密钥** — API 密钥
2. 点击「AI 增强匹配」对基础匹配未命中的记录进行二次匹配
3. 匹配结果会写入行级缓存，后续相同配置的匹配可免 API 调用直接命中

### AI API 兼容性

| 服务 | 端点示例 | 模型示例 |
|------|---------|---------|
| Deepseek | （留空使用默认） | `deepseek-chat` |
| OpenAI | `https://api.openai.com` | `gpt-4o` |
| Ollama 本地 | `http://localhost:11434` | `llama3` |
| vLLM | `http://localhost:8000` | 部署时指定 |

端点和模型均自动保存，下次启动恢复。

## 核心优化

- **AICache O(1) 查找** — map 索引替代线性扫描
- **预计算 B 列** — 清洗值、时间、提取值在匹配循环外预计算，避免 O(n×m) 次 regex/时间解析
- **Levenshtein 单次 rune 转换** — 消除 calcSimilarity → levenshteinDistance 的重复转换
- **RWMutex 读写分离** — 导出/表头读取不阻塞匹配
- **超时保护** — 单次匹配最长 10 分钟，内层循环每 500 次迭代检查

## 项目结构

```
data-matcher/
├── app.go                    # 核心逻辑（匹配引擎、文件读写、AI 调用、缓存）
├── main.go                   # 应用入口
├── wails.json                # Wails 项目配置
├── go.mod / go.sum           # Go 依赖
├── frontend/
│   ├── src/
│   │   ├── App.vue           # 主界面（Vue 单文件组件）
│   │   ├── style.css         # 全局样式
│   │   └── main.js           # Vue 入口
│   ├── wailsjs/              # Wails 自动生成绑定（需提交）
│   ├── index.html
│   ├── vite.config.js
│   └── package.json
└── build/                    # 构建输出（gitignore）
    └── bin/
        └── data-matcher.exe
```
