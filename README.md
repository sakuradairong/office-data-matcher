# 数据智能匹配工具 (Data Matcher)

基于 **Wails v2** (Go + Vue 3) 的桌面端数据匹配工具，支持 Excel/CSV 文件的智能列映射、模糊匹配与 AI 增强匹配。

## 功能特性

- 📂 **多文件支持** — 读取 `.xlsx` / `.xls` / `.csv` 格式
- 🔗 **动态列映射** — 自动解析表头，前端动态选择匹配列、时间列、提取列
- 🧹 **正则清洗** — 自定义正则剔除干扰字符（默认保留纯中文）
- ⏱ **时间窗口剪枝** — 按时间差过滤候选记录，提升匹配效率
- 📊 **Levenshtein 模糊匹配** — 基于编辑距离的相似度计算
- 🤖 **Deepseek AI 增强** — 对基础匹配未命中的记录，调用 Deepseek API 二次匹配
- 📤 **结果导出** — 支持导出为 Excel (`.xlsx`) 格式

## 技术栈

| 层级     | 技术                          |
| -------- | ----------------------------- |
| 后端     | Go 1.24 + Wails v2.12.0       |
| 前端     | Vue 3 (Composition API) + Vite |
| Excel    | excelize v2.10.1               |
| AI API   | Deepseek Chat API              |

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
# 安装前端依赖
cd frontend && npm install

# 启动热重载开发服务器
wails dev
```

开发模式下：
- Vite 热重载服务器运行在 `http://localhost:5173`
- Go 方法浏览器调试入口 `http://localhost:34115`

### 构建

```bash
wails build
```

构建产物位于 `build/bin/` 目录。

## 使用指南

1. 点击「选择文件」分别加载 A 表（基准表）和 B 表（数据源表）
2. 自动识别表头后，在下拉框中配置列映射：
   - **匹配列** — 用于模糊匹配的文本列
   - **时间列** — 可选，用于时间窗口剪枝
   - **提取列** — 从 B 表提取到结果的目标列
3. 点击「开始智能匹配」运行基础算法匹配
4. 可选：配置 Deepseek API 密钥后使用「AI 增强匹配」补充未命中记录
5. 点击「导出结果」保存为 Excel 文件

## 项目结构

```
data-matcher/
├── app.go              # 核心逻辑（匹配引擎、文件读写、AI 调用）
├── main.go             # 应用入口
├── wails.json          # Wails 项目配置
├── go.mod / go.sum     # Go 依赖
├── frontend/
│   ├── src/
│   │   ├── App.vue     # 主界面（Vue 组件）
│   │   ├── style.css   # 全局样式
│   │   └── main.js     # Vue 入口
│   ├── index.html
│   ├── vite.config.js
│   └── package.json
└── build/              # 构建配置（Windows/macOS 安装包）
```
