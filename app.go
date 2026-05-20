package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xuri/excelize/v2"
)

// ---------- 数据结构 ----------

// MatchResult 匹配结果
type MatchResult struct {
	RowAData       []string `json:"rowAData"`    // A 表原始所有列
	RowBKey        string   `json:"rowBKey"`     // B 表匹配列的值
	ExtractValue   string   `json:"extractValue"` // 从 B 表提取的目标列值
	TimeDiff       string   `json:"timeDiff"`
	SimilarityScore float64 `json:"similarityScore"`
	AIMatched      bool     `json:"aiMatched"`
}

// ProgressPayload 进度信息
type ProgressPayload struct {
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Message string `json:"message"`
	Phase   string `json:"phase"` // reading / matching / ai-enhancing / done
}

// MatchConfig 前端传递的完整匹配配置
type MatchConfig struct {
	// 文件路径
	FileAPath string `json:"fileAPath"`
	FileBPath string `json:"fileBPath"`

	// A 表列索引（-1 表示不使用）
	ColAMatchIndex int `json:"colAMatchIndex"` // A 表匹配列
	ColATimeIndex  int `json:"colATimeIndex"`  // A 表时间列（可选，-1 跳过时间剪枝）

	// B 表列索引
	ColBMatchIndex   int `json:"colBMatchIndex"`   // B 表匹配列
	ColBTimeIndex    int `json:"colBTimeIndex"`    // B 表时间列（可选，-1 跳过时间剪枝）
	ColBExtractIndex int `json:"colBExtractIndex"` // B 表要提取的目标列

	// 清洗与匹配参数
	RegexPattern string  `json:"regexPattern"` // 空字符串 = 跳过清洗
	TimeWindow   float64 `json:"timeWindow"`   // 小时
	Threshold    float64 `json:"threshold"`    // 0.0 - 1.0

	// 扩展选项
	AllMatches    bool   `json:"allMatches"`    // true=返回该A行所有匹配(>=阈值)而非仅最佳
	CaseSensitive bool   `json:"caseSensitive"` // true=大小写敏感匹配
	SortBy        string `json:"sortBy"`        // "similarity" / "timeDiff" / ""=不排序
	MaxPreview    int    `json:"maxPreview"`    // 调试日志中打印的前 N 条比对详情，0=不打印
	ExportFormat  string `json:"exportFormat"`  // "xlsx"(默认) / "csv"
	IncludeHeader bool   `json:"includeHeader"` // 导出时是否包含表头行
}

// ---------- OpenAI 兼容 API 类型 ----------

type deepseekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepseekRequest struct {
	Model       string            `json:"model"`
	Messages    []deepseekMessage `json:"messages"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
}

type deepseekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ---------- AI 缓存 ----------

// AICacheInfo 缓存信息（前端展示用）
type AICacheInfo struct {
	Count    int    `json:"count"`
	FilePath string `json:"filePath"`
}

// AICacheEntry 单条缓存记录（批量 prompt → 响应）
type AICacheEntry struct {
	PromptHash string `json:"promptHash"`
	Response   string `json:"response"`
	CreatedAt  int64  `json:"createdAt"`
}

// AIRowCacheEntry 单行 AI 匹配缓存（跨批次复用）
type AIRowCacheEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"` // AI 匹配到的 extractValue
	CreatedAt int64  `json:"createdAt"`
}

// AICache AI 响应缓存（持久化到临时文件）
type AICache struct {
	Entries       []AICacheEntry    `json:"entries"`
	RowEntries    []AIRowCacheEntry `json:"rowEntries"`
	entriesIdx    map[string]int    // promptHash → index in Entries (O(1) lookup)
	rowEntriesIdx map[string]int    // key → index in RowEntries (O(1) lookup)
	mu            sync.RWMutex
	filePath      string
	maxSize       int // 批量缓存最大条目数
	maxRowSize    int // 行级缓存最大条目数
}

// cacheFileName 缓存文件名
const cacheFileName = "data-matcher-ai-cache.json"

// 默认常量
const (
	defaultThreshold     = 0.65
	defaultTimeWindowH   = 12.0
	defaultBatchSize     = 8
	defaultMaxPreview    = 3
	defaultMaxBNoTime    = 200
	defaultAIWindowPadH  = 3.0
	maxPromptBChars      = 80000 // B 表数据在 prompt 中的最大字符数
	maxMatchDuration     = 10 * time.Minute // 单次匹配最大执行时间
)
// rebuildIndexes 从切片重建索引 map（反序列化或裁剪后调用）
func (c *AICache) rebuildIndexes() {
	c.entriesIdx = make(map[string]int, len(c.Entries))
	for i := range c.Entries {
		c.entriesIdx[c.Entries[i].PromptHash] = i
	}
	c.rowEntriesIdx = make(map[string]int, len(c.RowEntries))
	for i := range c.RowEntries {
		c.rowEntriesIdx[c.RowEntries[i].Key] = i
	}
}

// newAICache 创建缓存实例并加载已有数据
func newAICache() *AICache {
	c := &AICache{
		filePath:   filepath.Join(os.TempDir(), cacheFileName),
		maxSize:    500,
		maxRowSize: 5000,
	}
	c.loadFromFile()
	return c
}

// loadFromFile 从磁盘加载缓存
func (c *AICache) loadFromFile() {
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		// 文件不存在或无法读取，从空缓存开始
		if !os.IsNotExist(err) {
			fmt.Printf("[CACHE] 读取缓存文件失败: %v\n", err)
		}
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := json.Unmarshal(data, c); err != nil {
		fmt.Printf("[CACHE] 解析缓存文件失败，重置为空: %v\n", err)
		c.Entries = nil
		c.RowEntries = nil
	}
	c.rebuildIndexes()
}

// saveToFile 将缓存写入磁盘
func (c *AICache) saveToFile() {
	c.mu.RLock()
	data, err := json.Marshal(c)
	c.mu.RUnlock()
	if err != nil {
		fmt.Printf("[CACHE] 序列化缓存失败: %v\n", err)
		return
	}
	if err := os.WriteFile(c.filePath, data, 0600); err != nil {
		fmt.Printf("[CACHE] 写入缓存文件失败: %v\n", err)
	}
}

// get 根据 hash 查找缓存，命中返回响应，否则返回空
func (c *AICache) get(hash string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if idx, ok := c.entriesIdx[hash]; ok && idx < len(c.Entries) && c.Entries[idx].PromptHash == hash {
		return c.Entries[idx].Response, true
	}
	return "", false
}

// put 存入一条缓存（线程安全 + 自动裁剪）
func (c *AICache) put(hash, response string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 去重：如果已存在则覆盖
	if idx, ok := c.entriesIdx[hash]; ok && idx < len(c.Entries) && c.Entries[idx].PromptHash == hash {
		c.Entries[idx].Response = response
		c.Entries[idx].CreatedAt = time.Now().Unix()
		return
	}

	// 新增条目
	idx := len(c.Entries)
	c.Entries = append(c.Entries, AICacheEntry{
		PromptHash: hash,
		Response:   response,
		CreatedAt:  time.Now().Unix(),
	})
	c.entriesIdx[hash] = idx

	// 超过上限则删除最旧的条目
	if len(c.Entries) > c.maxSize {
		sort.Slice(c.Entries, func(i, j int) bool {
			return c.Entries[i].CreatedAt > c.Entries[j].CreatedAt
		})
		c.Entries = c.Entries[:c.maxSize]
		c.rebuildIndexes()
	}
}

// clear 清空所有缓存
func (c *AICache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Entries = nil
	c.RowEntries = nil
	c.entriesIdx = nil
	c.rowEntriesIdx = nil
	_ = os.Remove(c.filePath)
}

// stat 返回缓存统计
func (c *AICache) stat() (count int, path string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Entries) + len(c.RowEntries), c.filePath
}

// rowKey 为单行匹配构建缓存键
func (a *App) buildRowCacheKey(matchValue, timeStr string, config MatchConfig) string {
	parts := fmt.Sprintf("%s|%s|%s|%.1f|%s",
		matchValue, timeStr, config.RegexPattern, config.TimeWindow,
		filepath.Base(config.FileBPath))
	h := sha256.Sum256([]byte(parts))
	return hex.EncodeToString(h[:])
}

// getRow 查找行级缓存
func (c *AICache) getRow(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if idx, ok := c.rowEntriesIdx[key]; ok && idx < len(c.RowEntries) && c.RowEntries[idx].Key == key {
		return c.RowEntries[idx].Value, true
	}
	return "", false
}

// putRow 存入行级缓存（线程安全 + 自动裁剪）
func (c *AICache) putRow(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 去重：更新已存在的条目
	if idx, ok := c.rowEntriesIdx[key]; ok && idx < len(c.RowEntries) && c.RowEntries[idx].Key == key {
		c.RowEntries[idx].Value = value
		c.RowEntries[idx].CreatedAt = time.Now().Unix()
		return
	}

	// 新增条目
	idx := len(c.RowEntries)
	c.RowEntries = append(c.RowEntries, AIRowCacheEntry{
		Key:       key,
		Value:     value,
		CreatedAt: time.Now().Unix(),
	})
	c.rowEntriesIdx[key] = idx

	// 超过上限则删除最旧的条目
	if len(c.RowEntries) > c.maxRowSize {
		sort.Slice(c.RowEntries, func(i, j int) bool {
			return c.RowEntries[i].CreatedAt > c.RowEntries[j].CreatedAt
		})
		c.RowEntries = c.RowEntries[:c.maxRowSize]
		c.rebuildIndexes()
	}
}

// matchPrep 匹配准备的中间结果
type matchPrep struct {
	dataA, dataB   [][]string
	reg            *regexp.Regexp
	timeWindow     float64
	threshold      float64
	windowDuration time.Duration
}

// ---------- App 结构体 ----------

type App struct {
	ctx         context.Context
	apiKey      string // AI API 密钥（兼容 OpenAI/Deepseek/本地模型）
	apiEndpoint string // API 端点（默认 https://api.deepseek.com/v1/chat/completions）
	apiModel    string // 模型名称（默认 deepseek-chat）
	aiCache     *AICache

	// 最近一次匹配的配置和表头（供导出使用）
	dataMu      sync.RWMutex
	lastConfig  MatchConfig
	headersA    []string
	headersB    []string
}

// NewApp 创建 App 实例
func NewApp() *App {
	return &App{
		aiCache: newAICache(),
	}
}

// startup 保存上下文
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	count, path := a.aiCache.stat()
	fmt.Printf("[CACHE] AI 缓存已加载，当前 %d 条缓存记录 (文件: %s)\n", count, path)
}

// emitProgress 向前端发送进度事件
func (a *App) emitProgress(current, total int, message, phase string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "match-progress", ProgressPayload{
		Current: current,
		Total:   total,
		Message: message,
		Phase:   phase,
	})
}

// SetAIConfig 统一设置 AI API 配置（端点、模型、密钥）
func (a *App) SetAIConfig(endpoint, model, key string) string {
	if endpoint != "" {
		a.apiEndpoint = strings.TrimSpace(endpoint)
	}
	if model != "" {
		a.apiModel = strings.TrimSpace(model)
	}
	if key != "" {
		a.apiKey = strings.TrimSpace(key)
	}
	return fmt.Sprintf("AI 配置已更新 (端点=%s, 模型=%s)", a.apiEndpoint, a.apiModel)
}

// SetAPIKey 设置 AI API 密钥（仅保存在内存中）
func (a *App) SetAPIKey(key string) string {
	a.apiKey = strings.TrimSpace(key)
	if a.apiKey == "" {
		return "已清除 AI API 密钥"
	}
	return "AI API 密钥已设置"
}

// GetAIStatus 返回 AI API 配置状态
func (a *App) GetAIStatus() map[string]string {
	return map[string]string{
		"ready":    fmt.Sprintf("%v", a.apiKey != ""),
		"endpoint": a.apiEndpoint,
		"model":    a.apiModel,
	}
}

// ClearAICache 清除所有 AI 缓存
func (a *App) ClearAICache() string {
	before, _ := a.aiCache.stat()
	a.aiCache.clear()
	return fmt.Sprintf("已清除 %d 条 AI 缓存记录", before)
}

// GetAICacheInfo 返回 AI 缓存信息（条目数、文件路径）
func (a *App) GetAICacheInfo() AICacheInfo {
	count, path := a.aiCache.stat()
	return AICacheInfo{Count: count, FilePath: path}
}

// ---------- 文件选择对话框 ----------

// OpenMonthlyReport 打开文件对话框选择月报
func (a *App) OpenMonthlyReport() (string, error) {
	file, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择月报文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel / CSV 文件 (*.xlsx, *.xls, *.csv)", Pattern: "*.xlsx;*.xls;*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	return file, nil
}

// OpenDailyReport 打开文件对话框选择日报
func (a *App) OpenDailyReport() (string, error) {
	file, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择日报文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel / CSV 文件 (*.xlsx, *.xls, *.csv)", Pattern: "*.xlsx;*.xls;*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	return file, nil
}

// OpenFileA 打开文件对话框选择 A 表（基准表）
func (a *App) OpenFileA() (string, error) {
	return a.openFileDialog("选择 A 表文件（基准表）")
}

// OpenFileB 打开文件对话框选择 B 表（数据源表）
func (a *App) OpenFileB() (string, error) {
	return a.openFileDialog("选择 B 表文件（数据源表）")
}

func (a *App) openFileDialog(title string) (string, error) {
	file, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel / CSV 文件 (*.xlsx, *.xls, *.csv)", Pattern: "*.xlsx;*.xls;*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	return file, nil
}

// ParseHeaders 读取文件第一行作为表头数组返回给前端，用于动态渲染列映射下拉框
func (a *App) ParseHeaders(filePath string) ([]string, error) {
	if filePath == "" {
		return nil, fmt.Errorf("文件路径为空")
	}
	allRows, err := a.readRawRows(filePath)
	if err != nil {
		return nil, err
	}
	if len(allRows) == 0 {
		return nil, fmt.Errorf("文件为空，无表头")
	}
	headers := allRows[0]
	// TrimSpace 每个表头
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	fmt.Printf("[DEBUG] ParseHeaders: '%s' → %d 列 %v\n", filepath.Base(filePath), len(headers), headers)
	return headers, nil
}

var nonChineseRegex = regexp.MustCompile(`[^\p{Han}]+`)

// CleanString 剔除字符串中的所有非中文字符，仅保留纯中文字符
func (a *App) CleanString(input string) string {
	return nonChineseRegex.ReplaceAllString(input, "")
}

// ---------- 健壮的时间解析 ----------

// 多种时间格式，覆盖月报和日报的不同格式
var timeFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006-1-2 15:04:05",
	"2006-1-2 15:04",
	"2006/1/2 15:04:05",
	"2006/1/2 15:04",
	"2006-01-02T15:04:05",
	"2006/01/02T15:04:05",
	"01/02/2006 15:04",
	"1/2/2006 15:04",
	"2006-01-02",
	"2006/01/02",
}

// parseTimeFlexible 使用多种格式尝试解析时间字符串
func parseTimeFlexible(timeStr string) (time.Time, error) {
	timeStr = strings.TrimSpace(timeStr)
	for _, format := range timeFormats {
		if t, err := time.Parse(format, timeStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间格式: %s", timeStr)
}

// ---------- Levenshtein 距离算法 ----------

func levenshteinDistance(runes1, runes2 []rune) int {
	m, n := len(runes1), len(runes2)

	// 使用一维数组优化空间复杂度
	dp := make([]int, n+1)
	for j := range dp {
		dp[j] = j
	}

	for i := 1; i <= m; i++ {
		prev := dp[0]
		dp[0] = i
		for j := 1; j <= n; j++ {
			temp := dp[j]
			cost := 1
			if runes1[i-1] == runes2[j-1] {
				cost = 0
			}
			dp[j] = min(dp[j]+1, min(dp[j-1]+1, prev+cost))
			prev = temp
		}
	}
	return dp[n]
}


// CalculateSimilarity 计算清洗后中文名称的相似度（基于 Levenshtein 距离归一化）
func (a *App) CalculateSimilarity(s1, s2 string) float64 {
	return calcSimilarity(s1, s2, nonChineseRegex, false)
}

// calcSimilarity 带自定义正则的相似度计算；reg 为 nil 时不做清洗直接比对
func calcSimilarity(s1, s2 string, reg *regexp.Regexp, caseSensitive bool) float64 {
	clean1 := s1
	clean2 := s2
	if reg != nil {
		clean1 = reg.ReplaceAllString(s1, "")
		clean2 = reg.ReplaceAllString(s2, "")
	}
	if !caseSensitive {
		clean1 = strings.ToLower(clean1)
		clean2 = strings.ToLower(clean2)
	}

	r1 := []rune(clean1)
	r2 := []rune(clean2)

	if len(r1) == 0 && len(r2) == 0 {
		return 1.0
	}
	if len(r1) == 0 || len(r2) == 0 {
		return 0.0
	}

	dist := levenshteinDistance(r1, r2)
	maxLen := math.Max(float64(len(r1)), float64(len(r2)))
	return 1.0 - float64(dist)/maxLen
}

// cleanWithRegex 使用自定义正则清洗字符串；reg 为 nil 时返回原文
func cleanWithRegex(input string, reg *regexp.Regexp) string {
	if reg == nil {
		return input
	}
	return reg.ReplaceAllString(input, "")
}
// similarityFromCleaned 基于已清洗字符串计算相似度（跳过 regex 步骤，避免重复清洗）
func similarityFromCleaned(clean1, clean2 string, caseSensitive bool) float64 {
	if !caseSensitive {
		clean1 = strings.ToLower(clean1)
		clean2 = strings.ToLower(clean2)
	}
	r1 := []rune(clean1)
	r2 := []rune(clean2)
	if len(r1) == 0 && len(r2) == 0 {
		return 1.0
	}
	if len(r1) == 0 || len(r2) == 0 {
		return 0.0
	}
	dist := levenshteinDistance(r1, r2)
	maxLen := math.Max(float64(len(r1)), float64(len(r2)))
	return 1.0 - float64(dist)/maxLen
}

// ---------- 文件读取（通用）----------

// readRawRows 读取 Excel/CSV 文件，返回原始二维字符串切片（row[0] = 表头）
func (a *App) readRawRows(path string) ([][]string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return a.readCSVRaw(path)
	default:
		return a.readExcelRaw(path)
	}
}

func (a *App) readCSVRaw(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 CSV 文件失败: %v", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 文件失败: %v", err)
	}
	if len(allRows) < 2 {
		return nil, fmt.Errorf("CSV 文件至少需要标题行和一条数据")
	}
	for i := range allRows {
		for j := range allRows[i] {
			allRows[i][j] = strings.TrimSpace(allRows[i][j])
		}
	}
	return allRows, nil
}

func (a *App) readExcelRaw(path string) ([][]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("打开 Excel 文件失败: %v", err)
	}
	defer f.Close()

	sheetName := f.GetSheetName(0)
	allRows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("读取工作表失败: %v", err)
	}
	if len(allRows) < 2 {
		return nil, fmt.Errorf("Excel 文件至少需要标题行和一条数据")
	}
	return allRows, nil
}

// getCell 安全获取行中指定索引的单元格值，越界返回空字符串
func getCell(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}


// prepareMatch 编译正则、读取文件、初始化默认值（RunMatch / RunMatchWithAI 共用）
func (a *App) prepareMatch(config MatchConfig) (*matchPrep, error) {
	reg, err := compileRegex(config.RegexPattern)
	if err != nil {
		return nil, err
	}

	tw := config.TimeWindow
	if tw <= 0 {
		tw = defaultTimeWindowH
	}
	th := config.Threshold
	if th <= 0 {
		th = defaultThreshold
	}

	a.emitProgress(0, 100, "正在读取 A 表...", "reading")
	rowsA, err := a.readRawRows(config.FileAPath)
	if err != nil {
		return nil, fmt.Errorf("读取 A 表失败: %v", err)
	}
	a.emitProgress(0, 100, "正在读取 B 表...", "reading")
	rowsB, err := a.readRawRows(config.FileBPath)
	if err != nil {
		return nil, fmt.Errorf("读取 B 表失败: %v", err)
	}
	if len(rowsA) < 2 {
		return nil, fmt.Errorf("A 表无有效数据行")
	}
	if len(rowsB) < 2 {
		return nil, fmt.Errorf("B 表无有效数据行")
	}

	a.dataMu.Lock()
	a.headersA = rowsA[0]
	a.headersB = rowsB[0]
	a.lastConfig = config
	a.dataMu.Unlock()

	return &matchPrep{
		dataA:          rowsA[1:],
		dataB:          rowsB[1:],
		reg:            reg,
		timeWindow:     tw,
		threshold:      th,
		windowDuration: time.Duration(tw * float64(time.Hour)),
	}, nil
}

// RunMatch 接收完整 MatchConfig，按列索引执行通用匹配
// RunMatch 接收完整 MatchConfig，按列索引执行通用匹配
func (a *App) RunMatch(config MatchConfig) ([]MatchResult, error) {
	prep, err := a.prepareMatch(config)
	if err != nil {
		return nil, err
	}
	return a.runMatchOnData(prep, config)
}

// runMatchOnData 在已读取的数据上执行匹配
func (a *App) runMatchOnData(prep *matchPrep, config MatchConfig) ([]MatchResult, error) {
	useTime := config.ColATimeIndex >= 0 && config.ColBTimeIndex >= 0
	totalA := len(prep.dataA)
	var results []MatchResult
	startTime := time.Now() // 超时保护

	useAllMatches := config.AllMatches
	maxPreview := config.MaxPreview
	if maxPreview <= 0 {
		maxPreview = defaultMaxPreview
	}

	// 预计算 B 表清洗后的匹配值，避免内层循环中重复 regex 替换（O(n*m)→O(n+m)）
	totalB := len(prep.dataB)
	cleanedBMatch := make([]string, totalB)
	origBMatch := make([]string, totalB)
	parsedBTime := make([]time.Time, totalB)
	hasBTime := make([]bool, totalB)
	bExtractVal := make([]string, totalB)
	for bIdx, rowB := range prep.dataB {
		matchStrB := getCell(rowB, config.ColBMatchIndex)
		origBMatch[bIdx] = matchStrB
		if matchStrB == "" {
			cleanedBMatch[bIdx] = ""
		} else {
			cleanedBMatch[bIdx] = cleanWithRegex(matchStrB, prep.reg)
		}
		if useTime {
			t, err := parseTimeFlexible(getCell(rowB, config.ColBTimeIndex))
			if err == nil {
				parsedBTime[bIdx] = t
				hasBTime[bIdx] = true
			}
		}
		bExtractVal[bIdx] = getCell(rowB, config.ColBExtractIndex)
	}
	for i, rowA := range prep.dataA {
		// 定期检查超时 / 取消
		if time.Since(startTime) > maxMatchDuration {
			return results, fmt.Errorf("匹配超时（超过 %v）", maxMatchDuration)
		}
		if a.ctx != nil {
			select {
			case <-a.ctx.Done():
				return results, a.ctx.Err()
			default:
			}
		}

		if i%10 == 0 || i == totalA-1 {
			pct := (i + 1) * 100 / totalA
			a.emitProgress(i+1, totalA,
				fmt.Sprintf("匹配中 %d/%d (%d%%)...", i+1, totalA, pct), "matching")
		}

		matchStrA := getCell(rowA, config.ColAMatchIndex)
		if matchStrA == "" {
			continue
		}

		var timeA time.Time
		var hasTimeA bool
		if useTime {
			t, err := parseTimeFlexible(getCell(rowA, config.ColATimeIndex))
			if err == nil {
				timeA = t
				hasTimeA = true
			}
		}

		cleanA := cleanWithRegex(matchStrA, prep.reg)
		// 收集该 A 行的所有候选匹配
		var candidates []MatchResult

	bCheck := 0
	for bIdx := range prep.dataB {
		bCheck++
		if bCheck%500 == 0 && time.Since(startTime) > maxMatchDuration {
			return results, fmt.Errorf("匹配超时（超过 %v）", maxMatchDuration)
		}
		matchStrB := origBMatch[bIdx]
		if matchStrB == "" {
			continue
		}

		var timeDiff time.Duration
	if hasTimeA && useTime {
		if !hasBTime[bIdx] {
			continue
		}
		tB := parsedBTime[bIdx]
		td := timeA.Sub(tB)
		if td < -prep.windowDuration || td > prep.windowDuration {
			continue
		}
		timeDiff = td
	}

		cleanB := cleanedBMatch[bIdx]
		if cleanA == "" || cleanB == "" {
			continue
		}

		similarity := similarityFromCleaned(cleanA, cleanB, config.CaseSensitive)

			if i < maxPreview {
				fmt.Printf("[DEBUG] | A[%d]='%s'→'%s' | B='%s'→'%s' | 相似度=%.4f\n",
					i, matchStrA, cleanA, matchStrB, cleanB, similarity)
			}

			if similarity >= prep.threshold {
				mr := MatchResult{
					RowAData:        rowA,
					RowBKey:         matchStrB,
				ExtractValue:    bExtractVal[bIdx],
					TimeDiff:        formatTimeDiff(timeDiff),
					SimilarityScore: math.Round(similarity*10000) / 10000,
					AIMatched:       false,
				}
				if useAllMatches {
					candidates = append(candidates, mr)
				} else {
					if len(candidates) == 0 || similarity > candidates[0].SimilarityScore {
						candidates = []MatchResult{mr}
					}
					// B7: 完美匹配时提前退出
					if similarity == 1.0 {
						break
					}
				}
			}
		}

		if len(candidates) > 0 {
			if i < maxPreview {
				for _, c := range candidates {
					fmt.Printf("[DEBUG] ✓ 命中 | A='%s'→B='%s' | 相似度=%.4f\n",
						matchStrA, c.RowBKey, c.SimilarityScore)
				}
			}
			results = append(results, candidates...)
		}
	}

	// 结果排序
	switch config.SortBy {
	case "similarity":
		sort.Slice(results, func(i, j int) bool {
			return results[i].SimilarityScore > results[j].SimilarityScore
		})
	case "timeDiff":
		// B5: 使用原始数据重新解析时间差做数值排序
		sort.Slice(results, func(i, j int) bool {
			return parseTimeDiffDuration(results[i].TimeDiff) < parseTimeDiffDuration(results[j].TimeDiff)
		})
	}

	a.emitProgress(totalA, totalA,
		fmt.Sprintf("匹配完成！共匹配成功 %d 条记录", len(results)), "done")

	return results, nil
}

// compileRegex 编译正则，nil 表示跳过清洗
func compileRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	reg, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("正则表达式格式错误，请检查: %v", err)
	}
	fmt.Printf("[DEBUG] 使用正则: '%s'\n", pattern)
	return reg, nil
}

// parseTimeDiffDuration 将 TimeDiff 字符串（如 "1h30m"）解析为 time.Duration（用于排序）
func parseTimeDiffDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	sign := time.Duration(1)
	if s[0] == '-' {
		sign = -1
		s = s[1:]
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return sign * d
}

// RunMatchWithAI 执行基础匹配 + AI 增强匹配（配置驱动）
func (a *App) RunMatchWithAI(config MatchConfig) ([]MatchResult, error) {
	if a.apiKey == "" {
		return nil, fmt.Errorf("请先设置 AI API 密钥")
	}

	prep, err := a.prepareMatch(config)
	if err != nil {
		return nil, err
	}

	// 1. 先执行基础匹配
	results, err := a.runMatchOnData(prep, config)
	if err != nil {
		return nil, err
	}

	// 2. 找出未被基础匹配覆盖的 A 表行
	matchedSet := make(map[string]bool)
	for _, r := range results {
		matchedSet[strings.Join(r.RowAData, "\x00")] = true
	}

	var unmatchedA [][]string
	for _, row := range prep.dataA {
		if !matchedSet[strings.Join(row, "\x00")] {
			unmatchedA = append(unmatchedA, row)
		}
	}

	if len(unmatchedA) == 0 {
		a.emitProgress(1, 1, "全部已匹配，无需 AI 增强", "done")
		return results, nil
	}

	// 3. AI 增强匹配（先查行级缓存，减少 API 调用）
	useTime := config.ColATimeIndex >= 0 && config.ColBTimeIndex >= 0

	aiMatched := 0
	var failedBatches []int

	// 3a. 检查行级缓存，命中则直接加入结果
	var uncachedA [][]string
	cacheHits := 0
	for _, row := range unmatchedA {
		matchVal := getCell(row, config.ColAMatchIndex)
		timeStr := ""
		if useTime {
			timeStr = getCell(row, config.ColATimeIndex)
		}
		cacheKey := a.buildRowCacheKey(matchVal, timeStr, config)
		if cachedVal, ok := a.aiCache.getRow(cacheKey); ok {
			results = append(results, MatchResult{
				RowAData:        row,
				RowBKey:         "",
				ExtractValue:    cachedVal,
				SimilarityScore: 0,
				AIMatched:       true,
			})
			aiMatched++
			cacheHits++
		} else {
			uncachedA = append(uncachedA, row)
		}
	}

	if cacheHits > 0 {
		fmt.Printf("[CACHE] ✓ 行级缓存命中 %d 条，剩余 %d 条需 AI 处理\n", cacheHits, len(uncachedA))
	}

	if len(uncachedA) == 0 {
		a.emitProgress(1, 1,
			fmt.Sprintf("AI 增强完成！全部 %d 条命中缓存", cacheHits), "done")
		return results, nil
	}

	totalUnmatched := len(uncachedA)
	a.emitProgress(0, totalUnmatched,
		fmt.Sprintf("AI 增强匹配：%d 条命中缓存，%d 条需调用 AI...", cacheHits, totalUnmatched),
		"ai-enhancing")

	for batchStart := 0; batchStart < totalUnmatched; batchStart += defaultBatchSize {
		end := min(batchStart+defaultBatchSize, totalUnmatched)
		batchNum := (batchStart / defaultBatchSize) + 1

		a.emitProgress(batchStart+1, totalUnmatched,
			fmt.Sprintf("AI 分析中 %d/%d (第 %d 批)...", end, totalUnmatched, batchNum),
			"ai-enhancing")

		batch := uncachedA[batchStart:end]

		// 计算本批 A 表的时间范围
		var minTime, maxTime time.Time
		hasBatchTime := false
		if useTime {
			for _, row := range batch {
				t, err := parseTimeFlexible(getCell(row, config.ColATimeIndex))
				if err != nil {
					continue
				}
				if !hasBatchTime {
					minTime, maxTime = t, t
					hasBatchTime = true
				} else {
					if t.Before(minTime) {
						minTime = t
					}
					if t.After(maxTime) {
						maxTime = t
					}
				}
			}
		}

		// 过滤 B 表在时间窗口内的行（用户配置时间窗口 + 额外余量覆盖批次跨度）
		var relevantB [][]string
		if hasBatchTime && useTime {
			padding := prep.windowDuration + time.Duration(defaultAIWindowPadH)*time.Hour
			ws := minTime.Add(-padding)
			we := maxTime.Add(padding)
			for _, row := range prep.dataB {
				t, err := parseTimeFlexible(getCell(row, config.ColBTimeIndex))
				if err != nil || t.Before(ws) || t.After(we) {
					continue
				}
				relevantB = append(relevantB, row)
			}
		} else {
			// 无时间列时限制 B 表条数以控制 token 消耗
			maxB := min(defaultMaxBNoTime, len(prep.dataB))
			relevantB = prep.dataB[:maxB]
		}

		// 构建 AI 提示
		prompt := a.buildGenericAIPrompt(batch, relevantB, config, prep.windowDuration, hasBatchTime)
		aiResp, err := a.callAIAPI(prompt)
		if err != nil {
			fmt.Printf("[AI-WARN] 第 %d 批 API 调用失败: %v\n", batchNum, err)
			failedBatches = append(failedBatches, batchNum)
			continue
		}

		// 解析 AI 返回
		var matchResp struct {
			Matches []struct {
				Index int    `json:"index"`
				Value string `json:"value"`
			} `json:"matches"`
		}
		parseErr := json.Unmarshal([]byte(aiResp), &matchResp)
		if parseErr != nil {
			if idx := strings.Index(aiResp, "{"); idx >= 0 {
				if endIdx := strings.LastIndex(aiResp, "}"); endIdx > idx {
					parseErr = json.Unmarshal([]byte(aiResp[idx:endIdx+1]), &matchResp)
				}
			}
		}
		if parseErr != nil {
			fmt.Printf("[AI-WARN] 响应解析失败 (第 %d 批): %s\n   原始响应: %.200s\n",
				batchNum, parseErr.Error(), aiResp)
			failedBatches = append(failedBatches, batchNum)
			continue
		}

		for _, item := range matchResp.Matches {
			idx := item.Index
			val := strings.TrimSpace(item.Value)
			if idx < 0 || idx >= len(batch) || val == "" {
				continue
			}
			rowA := batch[idx]
			mr := MatchResult{
				RowAData:        rowA,
				RowBKey:         "",
				ExtractValue:    val,
				SimilarityScore: 0,
				AIMatched:       true,
			}
			results = append(results, mr)
				aiMatched++

				// 写入行级缓存
				matchVal := getCell(rowA, config.ColAMatchIndex)
				timeStr := ""
				if useTime {
					timeStr = getCell(rowA, config.ColATimeIndex)
				}
				cacheKey := a.buildRowCacheKey(matchVal, timeStr, config)
				a.aiCache.putRow(cacheKey, val)
		}
	}
	a.aiCache.saveToFile()

	// 构建完成消息
	msg := fmt.Sprintf("AI 增强完成！基础匹配 %d 条 + AI 补充 %d 条 = 共 %d 条",
		len(results)-aiMatched, aiMatched, len(results))
	if len(failedBatches) > 0 {
		msg += fmt.Sprintf("（警告：第 %v 批失败）", failedBatches)
	}
	a.emitProgress(totalUnmatched, totalUnmatched, msg, "done")

	return results, nil
}

// buildGenericAIPrompt 构建通用 AI 匹配提示词
func (a *App) buildGenericAIPrompt(unmatched, bRows [][]string, config MatchConfig, windowDuration time.Duration, hasTime bool) []deepseekMessage {
	var sb strings.Builder
	sb.WriteString("你是一个数据匹配专家。请根据以下 A 表记录，从 B 表数据中找出最匹配的记录。\n\n")
	sb.WriteString("匹配规则：\n")
	sb.WriteString("1. 根据文本相似度匹配（注意中文字段的核心含义，忽略字母数字前缀后缀）\n")
	if hasTime {
		sb.WriteString(fmt.Sprintf("2. 时间差应在 %.0f 小时内\n", windowDuration.Hours()))
	}
	sb.WriteString(fmt.Sprintf("3. 返回匹配到的 B 表记录的目标列值（第 %d 列）\n\n", config.ColBExtractIndex+1))

	sb.WriteString("请严格按照以下 JSON 格式返回结果：\n")
	sb.WriteString(`{"matches":[{"index":0,"value":"匹配到的目标列值"},{"index":1,"value":""}]}` + "\n")
	sb.WriteString("如果某条无法匹配，value 设为空字符串。\n\n")

	sb.WriteString(fmt.Sprintf("A 表记录（需要匹配，共 %d 条）：\n", len(unmatched)))
	for i, row := range unmatched {
		matchVal := getCell(row, config.ColAMatchIndex)
		sb.WriteString(fmt.Sprintf("- 索引 %d: 「%s」", i, matchVal))
		if hasTime {
			sb.WriteString(fmt.Sprintf(", 时间=%s", getCell(row, config.ColATimeIndex)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("\nB 表参考数据（共 %d 条）：\n", len(bRows)))
	truncated := false
	for i, row := range bRows {
		matchVal := getCell(row, config.ColBMatchIndex)
		extractVal := getCell(row, config.ColBExtractIndex)
		sb.WriteString(fmt.Sprintf("  「%s」 → 目标列值: 「%s」", matchVal, extractVal))
		if hasTime {
			sb.WriteString(fmt.Sprintf(", 时间=%s", getCell(row, config.ColBTimeIndex)))
		}
		sb.WriteString("\n")
		// 限制 B 表部分总字符数，防止 prompt 超出 token 限制
		if sb.Len() > maxPromptBChars {
			fmt.Printf("[AI-WARN] Prompt B 表数据超长 (%d 条，%d 字符)，截断于第 %d 条\n", len(bRows), sb.Len(), i)
			truncated = true
		}
		if truncated {
			sb.WriteString(fmt.Sprintf("  ... 已截断，省略 %d 条\n", len(bRows)-i-1))
			break
		}
	}

	sb.WriteString("\n请返回 JSON 格式的匹配结果。")

	return []deepseekMessage{
		{Role: "system", Content: "你是一个数据匹配专家。请严格按照 JSON 格式返回结果，不要添加额外说明。"},
		{Role: "user", Content: sb.String()},
	}
}

// ---------- AI API 调用 ----------

// hashPrompt 对 prompt 消息计算 SHA256（用于缓存键）
func hashPrompt(messages []deepseekMessage) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// callAIAPI 调用 OpenAI 兼容 API（Deepseek / OpenAI / 本地模型 等）
func (a *App) callAIAPI(messages []deepseekMessage) (string, error) {
	if a.apiKey == "" {
		return "", fmt.Errorf("请先设置 AI API 密钥")
	}

	// 默认值
	endpoint := strings.TrimRight(a.apiEndpoint, "/")
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/v1/chat/completions"
	} else if !strings.HasSuffix(endpoint, "/chat/completions") {
		// 自动补齐 OpenAI 兼容路径（用户只需填 base URL）
		endpoint += "/v1/chat/completions"
	}
	model := a.apiModel
	if model == "" {
		model = "deepseek-chat"
	}

	hash := hashPrompt(messages)

	// 先查缓存
	if cached, ok := a.aiCache.get(hash); ok {
		fmt.Printf("[CACHE] ✓ 命中 AI 缓存 (hash=%s)\n", hash[:12])
		return cached, nil
	}
	fmt.Printf("[CACHE] ✗ 缓存未命中 (hash=%s)，调用 %s...\n", hash[:12], endpoint)

	reqBody := deepseekRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.05,
		MaxTokens:   2048,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("调用 AI API 失败: %v", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取 AI 响应失败: %v", err)
	}
	var dr deepseekResponse
	if err := json.Unmarshal(respBytes, &dr); err != nil {
		return "", fmt.Errorf("解析 AI 响应失败: %v", err)
	}

	if dr.Error != nil {
		return "", fmt.Errorf("AI API 错误: %s", dr.Error.Message)
	}

	if len(dr.Choices) == 0 {
		return "", fmt.Errorf("AI 未返回有效结果")
	}

	result := strings.TrimSpace(dr.Choices[0].Message.Content)

	// 写入缓存并持久化
	a.aiCache.put(hash, result)
	a.aiCache.saveToFile()

	return result, nil
}


// formatTimeDiff 格式化时间差为可读字符串
func formatTimeDiff(d time.Duration) string {
	abs := d
	if abs < 0 {
		abs = -abs
	}
	hours := int(abs.Hours())
	mins := int(abs.Minutes()) % 60
	secs := int(abs.Seconds()) % 60

	sign := ""
	if d < 0 {
		sign = "-"
	}
	if hours > 0 {
		return fmt.Sprintf("%s%dh%dm%ds", sign, hours, mins, secs)
	} else if mins > 0 {
		return fmt.Sprintf("%s%dm%ds", sign, mins, secs)
	}
	return fmt.Sprintf("%s%ds", sign, secs)
}

// ---------- 导出结果 ----------

// ExportResults 将匹配结果导出为 Excel 或 CSV 文件
func (a *App) ExportResults(results []MatchResult) (string, error) {
	if len(results) == 0 {
		return "", fmt.Errorf("没有匹配结果可以导出")
	}

	a.dataMu.RLock()
	useCSV := a.lastConfig.ExportFormat == "csv"
	includeHdr := a.lastConfig.IncludeHeader
	a.dataMu.RUnlock()

	isCSV := useCSV
	ext := ".xlsx"
	filterDisplay := "Excel 文件 (*.xlsx)"
	filterPattern := "*.xlsx"
	if isCSV {
		ext = ".csv"
		filterDisplay = "CSV 文件 (*.csv)"
		filterPattern = "*.csv"
	}

	savePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "导出匹配结果",
		DefaultFilename: fmt.Sprintf("匹配结果_%s%s", time.Now().Format("20060102_150405"), ext),
		Filters: []runtime.FileFilter{
			{DisplayName: filterDisplay, Pattern: filterPattern},
		},
	})
	if err != nil {
		return "", fmt.Errorf("打开保存对话框失败: %v", err)
	}
	if savePath == "" {
		return "", nil
	}
	if !strings.HasSuffix(strings.ToLower(savePath), ext) {
		savePath += ext
	}

	if isCSV {
		return a.exportResultsCSV(results, savePath, includeHdr)
	}
	return a.exportResultsXLSX(results, savePath, includeHdr)
}

// exportHeaders 构建导出表头行（使用真实表头或回退默认）
func (a *App) exportHeaders(numACols int) []string {
	a.dataMu.RLock()
	hdrA := make([]string, len(a.headersA))
	copy(hdrA, a.headersA)
	a.dataMu.RUnlock()

	headers := make([]string, 0, numACols+1)
	if len(hdrA) >= numACols {
		for _, h := range hdrA[:numACols] {
			n := h
			if n == "" {
				n = fmt.Sprintf("Col%d", len(headers)+1)
			}
			headers = append(headers, n)
		}
	} else {
		for i := 0; i < numACols; i++ {
			headers = append(headers, fmt.Sprintf("A-Col%d", i+1))
		}
	}
	headers = append(headers, "匹配结果(由B表提取)")
	return headers
}

func (a *App) exportResultsXLSX(results []MatchResult, savePath string, includeHeader bool) (string, error) {
	f := excelize.NewFile()
	defer f.Close()
	sheetName := "匹配结果"
	f.SetSheetName("Sheet1", sheetName)

	numACols := len(results[0].RowAData)
	colLetter := func(n int) string { c, _ := excelize.ColumnNumberToName(n + 1); return c }

	headers := a.exportHeaders(numACols)
	extractCol := numACols

	// 表头
	if includeHeader {
		for i, h := range headers {
			f.SetCellValue(sheetName, fmt.Sprintf("%s1", colLetter(i)), h)
		}
		headerStyle, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Bold: true, Size: 12, Color: "FFFFFF"},
			Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"4472C4"}},
		})
		f.SetCellStyle(sheetName, "A1", fmt.Sprintf("%s1", colLetter(extractCol)), headerStyle)
		// 数据行样式（带边框和行号字体）
		dataStyle, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Size: 11},
			Border: []excelize.Border{
				{Type: "bottom", Color: "D9D9D9", Style: 1},
			},
		})
		firstDataRow := 2
		lastDataRow := len(results) + 1
		for ci := 0; ci <= numACols; ci++ {
			f.SetCellStyle(sheetName,
				fmt.Sprintf("%s%d", colLetter(ci), firstDataRow),
				fmt.Sprintf("%s%d", colLetter(ci), lastDataRow),
				dataStyle)
		}
	} else {
		dataStyle, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Size: 11},
			Border: []excelize.Border{
				{Type: "bottom", Color: "D9D9D9", Style: 1},
			},
		})
		lastDataRow := len(results)
		for ci := 0; ci <= numACols; ci++ {
			f.SetCellStyle(sheetName,
				fmt.Sprintf("%s%d", colLetter(ci), 1),
				fmt.Sprintf("%s%d", colLetter(ci), lastDataRow),
				dataStyle)
		}
	}

	// 数据行
	for i, r := range results {
		rowNum := i + 2
		if !includeHeader {
			rowNum = i + 1
		}
		for ci := 0; ci < numACols; ci++ {
			f.SetCellValue(sheetName, fmt.Sprintf("%s%d", colLetter(ci), rowNum), r.RowAData[ci])
		}
		f.SetCellValue(sheetName, fmt.Sprintf("%s%d", colLetter(extractCol), rowNum), r.ExtractValue)
	}

	// 列宽
	for ci := 0; ci <= numACols; ci++ {
		f.SetColWidth(sheetName, colLetter(ci), colLetter(ci), 22)
	}

	if err := f.SaveAs(savePath); err != nil {
		return "", fmt.Errorf("保存文件失败: %v", err)
	}
	return savePath, nil
}

func (a *App) exportResultsCSV(results []MatchResult, savePath string, includeHeader bool) (string, error) {
	var buf bytes.Buffer
	// 使用 UTF-8 BOM 帮助 Excel 正确识别编码
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	numACols := len(results[0].RowAData)
	headers := a.exportHeaders(numACols)

	// 表头行
	if includeHeader {
		for i, h := range headers {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(csvEscape(h))
		}
		buf.WriteByte('\n')
	}

	// 数据行
	for _, r := range results {
		for ci := 0; ci < numACols; ci++ {
			if ci > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(csvEscape(r.RowAData[ci]))
		}
		buf.WriteByte(',')
		buf.WriteString(csvEscape(r.ExtractValue))
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(savePath, buf.Bytes(), 0600); err != nil {
		return "", fmt.Errorf("保存 CSV 文件失败: %v", err)
	}
	return savePath, nil
}

// csvEscape 对 CSV 字段进行转义（含逗号或引号时包裹双引号）
func csvEscape(s string) string {
	if strings.ContainsAny(s, "\",\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
