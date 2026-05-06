package main

import (
	"bytes"
	"context"
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
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xuri/excelize/v2"
)

// ---------- 数据结构 ----------

// MonthlyRecord 月报记录（向后兼容）
type MonthlyRecord struct {
	CellName     string    `json:"cellName"`
	OccurTime    time.Time `json:"-"`
	OccurTimeStr string    `json:"occurTimeStr"`
}

// DailyRecord 日报记录（向后兼容）
type DailyRecord struct {
	CellID          string    `json:"cellId"`
	OccurTime       time.Time `json:"-"`
	OccurTimeStr    string    `json:"occurTimeStr"`
	InterruptReason string    `json:"interruptReason"`
}

// MatchResult 匹配结果（新旧字段兼容）
type MatchResult struct {
	// 新字段（通用化）
	RowAData    []string `json:"rowAData"`    // A 表原始所有列（新）
	RowBKey     string   `json:"rowBKey"`     // B 表匹配列的值（新）
	ExtractValue string  `json:"extractValue"` // 从 B 表提取的目标列值（新）

	// 旧字段（向后兼容）
	MonthlyCellName string `json:"monthlyCellName"`
	DailyCellID     string `json:"dailyCellId"`
	InterruptReason string `json:"interruptReason"`

	// 公共字段
	TimeDiff        string  `json:"timeDiff"`
	SimilarityScore float64 `json:"similarityScore"`
	AIMatched       bool    `json:"aiMatched"`
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

// ---------- Deepseek API 类型 ----------

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

// ---------- App 结构体 ----------

type App struct {
	ctx          context.Context
	deepseekKey  string
}

// NewApp 创建 App 实例
func NewApp() *App {
	return &App{}
}

// startup 保存上下文
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
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

// SetDeepseekAPIKey 设置 Deepseek API 密钥（仅保存在内存中）
func (a *App) SetDeepseekAPIKey(key string) string {
	a.deepseekKey = strings.TrimSpace(key)
	if a.deepseekKey == "" {
		return "已清除 Deepseek API 密钥"
	}
	return "Deepseek API 密钥已设置"
}

// GetDeepseekStatus 返回是否已配置 Deepseek API 密钥
func (a *App) GetDeepseekStatus() bool {
	return a.deepseekKey != ""
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

func levenshteinDistance(s1, s2 string) int {
	runes1 := []rune(s1)
	runes2 := []rune(s2)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// CalculateSimilarity 计算清洗后中文名称的相似度（基于 Levenshtein 距离归一化）
func (a *App) CalculateSimilarity(s1, s2 string) float64 {
	return calcSimilarity(s1, s2, nonChineseRegex)
}

// calcSimilarity 带自定义正则的相似度计算；reg 为 nil 时不做清洗直接比对
func calcSimilarity(s1, s2 string, reg *regexp.Regexp) float64 {
	clean1 := s1
	clean2 := s2
	if reg != nil {
		clean1 = reg.ReplaceAllString(s1, "")
		clean2 = reg.ReplaceAllString(s2, "")
	}

	r1 := []rune(clean1)
	r2 := []rune(clean2)

	if len(r1) == 0 && len(r2) == 0 {
		return 1.0
	}
	if len(r1) == 0 || len(r2) == 0 {
		return 0.0
	}

	dist := levenshteinDistance(clean1, clean2)
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
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 文件失败: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("CSV 文件至少需要标题行和一条数据")
	}

	var rows [][]string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := parseCSVLine(line)
		rows = append(rows, fields)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("CSV 文件无有效数据行")
	}
	return rows, nil
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

// parseCSVLine 简单 CSV 行解析（支持双引号包裹的字段）
func parseCSVLine(line string) []string {
	var fields []string
	var current strings.Builder
	inQuotes := false

	for _, ch := range line {
		switch {
		case ch == '"':
			inQuotes = !inQuotes
		case ch == ',' && !inQuotes:
			fields = append(fields, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}
	fields = append(fields, strings.TrimSpace(current.String()))
	return fields
}

// ---------- 动态表头索引映射 ----------

// findColumnIndexExact 在表头行中查找与候选词【精确相等】的第一列索引，未找到返回 -1
// 使用 strings.EqualFold 做大小写不敏感的精确匹配，两端 TrimSpace
func findColumnIndexExact(headers []string, candidates ...string) int {
	for i, h := range headers {
		trimmed := strings.TrimSpace(h)
		for _, c := range candidates {
			if strings.EqualFold(trimmed, strings.TrimSpace(c)) {
				return i
			}
		}
	}
	return -1
}

// ---------- 读取月报 ----------

func (a *App) readMonthlyReport(path string) ([]MonthlyRecord, error) {
	allRows, err := a.readRawRows(path)
	if err != nil {
		return nil, err
	}

	// 第 1 行是表头
	headers := allRows[0]
	fmt.Printf("[DEBUG] 月报全部表头 (共%d列): %v\n", len(headers), headers)

	// 动态找关键列的索引
	cellNameIdx := findColumnIndexExact(headers, "小区名称", "小区名", "cellname", "cell name", "小区")
	timeIdx := findColumnIndexExact(headers, "告警发生时间", "发生时间", "告警时间", "时间", "occurtime", "occur time")

	// 回退策略：找不到时用前两列
	if cellNameIdx == -1 && len(headers) >= 1 {
		cellNameIdx = 0
	}
	if timeIdx == -1 && len(headers) >= 2 {
		timeIdx = 1
	}

	fmt.Printf("[DEBUG] 月报列索引: 小区名称 idx=%d, 时间 idx=%d\n", cellNameIdx, timeIdx)
	if cellNameIdx == -1 || timeIdx == -1 {
		return nil, fmt.Errorf("无法识别月报表头，需要包含「小区名称」和「告警发生时间」列")
	}

	var records []MonthlyRecord
	for rowNum, row := range allRows[1:] {
		name := getCell(row, cellNameIdx)
		timeStr := getCell(row, timeIdx)
		if name == "" || timeStr == "" {
			continue
		}
		// 月报专用 Layout: 2006-01-02 15:04:05
		t, err := time.Parse("2006-01-02 15:04:05", timeStr)
		if err != nil {
			fmt.Printf("[DEBUG] 月报时间解析失败 L%d | 原始: '%s' | 错误: %v\n", rowNum+2, timeStr, err)
			continue
		}
		records = append(records, MonthlyRecord{
			CellName:     name,
			OccurTime:    t,
			OccurTimeStr: t.Format("2006-01-02 15:04:05"),
		})
	}
	fmt.Printf("[DEBUG] 月报有效记录数: %d (共 %d 行数据)\n", len(records), len(allRows)-1)
	return records, nil
}

// ---------- 读取日报 ----------

func (a *App) readDailyReport(path string) ([]DailyRecord, error) {
	allRows, err := a.readRawRows(path)
	if err != nil {
		return nil, err
	}

	// 第 1 行是表头
	headers := allRows[0]
	fmt.Printf("[DEBUG] 日报全部表头 (共%d列): %v\n", len(headers), headers)

	// 动态找关键列的索引
	cellIDIdx := findColumnIndexExact(headers, "小区号", "小区编号", "小区id", "cellid", "cell id", "小区")
	timeIdx := findColumnIndexExact(headers, "发生时间", "时间", "occurtime", "occur time")
	reasonIdx := findColumnIndexExact(headers, "中断原因", "原因", "reason", "中断", "中断原因", "failure reason")

	// 回退策略
	if cellIDIdx == -1 && len(headers) >= 1 {
		cellIDIdx = 0
	}
	if timeIdx == -1 && len(headers) >= 2 {
		timeIdx = 1
	}
	if reasonIdx == -1 && len(headers) >= 3 {
		reasonIdx = 2
	}

	fmt.Printf("[DEBUG] 日报列索引: 小区号 idx=%d, 时间 idx=%d, 中断原因 idx=%d\n", cellIDIdx, timeIdx, reasonIdx)
	if cellIDIdx == -1 || timeIdx == -1 || reasonIdx == -1 {
		return nil, fmt.Errorf("无法识别日报表头，需要包含「小区号」「发生时间」「中断原因」列")
	}

	var records []DailyRecord
	for rowNum, row := range allRows[1:] {
		cellID := getCell(row, cellIDIdx)
		timeStr := getCell(row, timeIdx)
		reason := getCell(row, reasonIdx)
		if cellID == "" || timeStr == "" {
			continue
		}
		// 日报专用 Layout: 2006/1/2 15:04（支持单双位数的月/日）
		t, err := time.Parse("2006/1/2 15:04", timeStr)
		if err != nil {
			fmt.Printf("[DEBUG] 日报时间解析失败 L%d | 原始: '%s' | 错误: %v\n", rowNum+2, timeStr, err)
			continue
		}
		records = append(records, DailyRecord{
			CellID:          cellID,
			OccurTime:       t,
			OccurTimeStr:    t.Format("2006-01-02 15:04:05"),
			InterruptReason: reason,
		})
	}
	fmt.Printf("[DEBUG] 日报有效记录数: %d (共 %d 行数据)\n", len(records), len(allRows)-1)
	return records, nil
}

// ---------- 核心匹配引擎 ----------

// StartMatching 执行多维智能匹配（带进度推送）
func (a *App) StartMatching(monthlyPath, dailyPath string) ([]MatchResult, error) {
	a.emitProgress(0, 100, "正在读取月报文件...", "reading")
	monthlyRecords, err := a.readMonthlyReport(monthlyPath)
	if err != nil {
		return nil, fmt.Errorf("读取月报失败: %v", err)
	}

	a.emitProgress(0, 100, "正在读取日报文件...", "reading")
	dailyRecords, err := a.readDailyReport(dailyPath)
	if err != nil {
		return nil, fmt.Errorf("读取日报失败: %v", err)
	}

	if len(monthlyRecords) == 0 {
		return nil, fmt.Errorf("月报中无有效记录（或时间解析失败）")
	}
	if len(dailyRecords) == 0 {
		return nil, fmt.Errorf("日报中无有效记录（或时间解析失败）")
	}

	twelveHours := 12 * time.Hour
	totalMonthly := len(monthlyRecords)
	var results []MatchResult

	for i, mr := range monthlyRecords {
		if i%10 == 0 || i == totalMonthly-1 {
			pct := (i + 1) * 100 / totalMonthly
			a.emitProgress(i+1, totalMonthly,
				fmt.Sprintf("正在匹配第 %d/%d 条月报记录 (%d%%)...", i+1, totalMonthly, pct),
				"matching")
		}

		var bestMatch *DailyRecord
		bestSimilarity := 0.0
		bestTimeDiff := time.Duration(0)
		cleanMonthly := a.CleanString(mr.CellName)

		for _, dr := range dailyRecords {
			// 步骤一：时间剪枝 — 保留时间差在 ±12h 内的记录
			timeDiff := mr.OccurTime.Sub(dr.OccurTime)
			if timeDiff < -twelveHours || timeDiff > twelveHours {
				continue
			}

			// 步骤二：计算清洗后中文名称的相似度
			cleanDaily := a.CleanString(dr.CellID)
			if cleanMonthly == "" || cleanDaily == "" {
				continue
			}

			similarity := a.CalculateSimilarity(mr.CellName, dr.CellID)

			if i < 5 {
				fmt.Printf("[DEBUG] 比对 | 月报='%s'→清洗='%s' | 日报='%s'→清洗='%s' | 时间差=%v | 相似度=%.4f\n",
					mr.CellName, cleanMonthly, dr.CellID, cleanDaily, timeDiff, similarity)
			}

			// 阈值 0.65：只保留高置信度匹配
			if similarity >= 0.65 && similarity > bestSimilarity {
				bestSimilarity = similarity
				bestMatch = &dr
				bestTimeDiff = timeDiff
			}
		}

		if bestMatch != nil {
			if i < 5 {
				fmt.Printf("[DEBUG] ✓ 命中 | 月报='%s'→日报='%s' | 相似度=%.4f | 时间差=%v\n",
					mr.CellName, bestMatch.CellID, bestSimilarity, bestTimeDiff)
			}
			results = append(results, MatchResult{
				MonthlyCellName: mr.CellName,
				DailyCellID:     bestMatch.CellID,
				TimeDiff:        formatTimeDiff(bestTimeDiff),
				SimilarityScore: math.Round(bestSimilarity*10000) / 10000,
				InterruptReason: bestMatch.InterruptReason,
				AIMatched:       false,
			})
		} else {
			if i < 5 {
				fmt.Printf("[DEBUG] ✗ 未命中 | 月报='%s'(清洗='%s') | 未找到匹配\n",
					mr.CellName, cleanMonthly)
			}
		}
	}

	a.emitProgress(totalMonthly, totalMonthly,
		fmt.Sprintf("匹配完成！共匹配成功 %d 条记录", len(results)), "done")

	return results, nil
}

// ---------- 通用匹配引擎 ----------

// RunMatch 接收完整 MatchConfig，按列索引执行通用匹配
func (a *App) RunMatch(config MatchConfig) ([]MatchResult, error) {
	// 1. 编译正则
	var reg *regexp.Regexp
	if config.RegexPattern != "" {
		var err error
		reg, err = regexp.Compile(config.RegexPattern)
		if err != nil {
			return nil, fmt.Errorf("正则表达式格式错误，请检查: %v", err)
		}
		fmt.Printf("[DEBUG] RunMatch 使用正则: '%s'\n", config.RegexPattern)
	} else {
		fmt.Printf("[DEBUG] RunMatch 跳过清洗（正则为空）\n")
	}

	// 2. 默认值兜底
	timeWindow := config.TimeWindow
	if timeWindow <= 0 {
		timeWindow = 12
	}
	threshold := config.Threshold
	if threshold <= 0 {
		threshold = 0.65
	}
	useTime := config.ColATimeIndex >= 0 && config.ColBTimeIndex >= 0

	// 3. 读取原始数据
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

	aHeaders := rowsA[0]
	_ = aHeaders // 保留表头引用（将来导出时可能用到）
	dataA := rowsA[1:]
	dataB := rowsB[1:]
	windowDuration := time.Duration(timeWindow * float64(time.Hour))
	totalA := len(dataA)
	var results []MatchResult

	useAllMatches := config.AllMatches
	maxPreview := config.MaxPreview
	if maxPreview <= 0 {
		maxPreview = 3
	}

	for i, rowA := range dataA {
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
			if err == nil { timeA = t; hasTimeA = true }
		}

		cleanA := cleanWithRegex(matchStrA, reg)
		// 收集该 A 行的所有候选匹配
		var candidates []MatchResult

		for _, rowB := range dataB {
			matchStrB := getCell(rowB, config.ColBMatchIndex)
			if matchStrB == "" { continue }

			var timeDiff time.Duration
			if hasTimeA && useTime {
				tB, err := parseTimeFlexible(getCell(rowB, config.ColBTimeIndex))
				if err != nil { continue }
				td := timeA.Sub(tB)
				if td < -windowDuration || td > windowDuration { continue }
				timeDiff = td
			}

			cleanB := cleanWithRegex(matchStrB, reg)
			if cleanA == "" || cleanB == "" { continue }

			similarity := calcSimilarity(matchStrA, matchStrB, reg)

			if i < maxPreview {
				fmt.Printf("[DEBUG] | A[%d]='%s'→'%s' | B='%s'→'%s' | 相似度=%.4f\n",
					i, matchStrA, cleanA, matchStrB, cleanB, similarity)
			}

			if similarity >= threshold {
				mr := MatchResult{
					RowAData:        rowA,
					RowBKey:         matchStrB,
					ExtractValue:    getCell(rowB, config.ColBExtractIndex),
					TimeDiff:        formatTimeDiff(timeDiff),
					SimilarityScore: math.Round(similarity*10000) / 10000,
					AIMatched:       false,
				}
				if useAllMatches {
					candidates = append(candidates, mr)
				} else if len(candidates) == 0 || similarity > candidates[0].SimilarityScore {
					candidates = []MatchResult{mr}
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
	if config.SortBy == "similarity" {
		sort.Slice(results, func(i, j int) bool {
			return results[i].SimilarityScore > results[j].SimilarityScore
		})
	} else if config.SortBy == "timeDiff" {
		sort.Slice(results, func(i, j int) bool {
			return results[i].TimeDiff < results[j].TimeDiff
		})
	}

	a.emitProgress(totalA, totalA,
		fmt.Sprintf("匹配完成！共匹配成功 %d 条记录", len(results)), "done")

	return results, nil
}

type rowAndScore struct {
	row   []string
	score float64
}

// ---------- Deepseek AI 增强匹配 ----------

// callDeepseekAPI 调用 Deepseek Chat API
func (a *App) callDeepseekAPI(messages []deepseekMessage) (string, error) {
	if a.deepseekKey == "" {
		return "", fmt.Errorf("请先设置 Deepseek API 密钥")
	}

	reqBody := deepseekRequest{
		Model:       "deepseek-chat",
		Messages:    messages,
		Temperature: 0.05,
		MaxTokens:   2048,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	httpReq, err := http.NewRequest("POST", "https://api.deepseek.com/v1/chat/completions",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.deepseekKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("调用 Deepseek API 失败: %v", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	var dr deepseekResponse
	if err := json.Unmarshal(respBytes, &dr); err != nil {
		return "", fmt.Errorf("解析 Deepseek 响应失败: %v", err)
	}

	if dr.Error != nil {
		return "", fmt.Errorf("Deepseek API 错误: %s", dr.Error.Message)
	}

	if len(dr.Choices) == 0 {
		return "", fmt.Errorf("Deepseek 未返回有效结果")
	}

	return strings.TrimSpace(dr.Choices[0].Message.Content), nil
}

// buildAIPrompt 构建 AI 匹配提示词
func buildAIPrompt(monthlyRecords []MonthlyRecord, dailyRecords []DailyRecord, batchStart, batchSize int) []deepseekMessage {
	end := batchStart + batchSize
	if end > len(monthlyRecords) {
		end = len(monthlyRecords)
	}
	batch := monthlyRecords[batchStart:end]

	var sb strings.Builder
	sb.WriteString(`你是一个通信网络数据匹配专家。请根据以下月报记录，从日报数据中找出最匹配的「中断原因」。

匹配规则：
1. 首先根据「发生时间」匹配，时间差应在 ±2 小时内
2. 然后根据「小区名称」匹配：小区名称可能包含字母数字前缀后缀（如 LTESF1_32、2100_2 等），请着重对比其中的中文字段
3. 如果找到匹配项，返回中断原因；如果找不到，返回空字符串

请严格按照以下 JSON 格式返回结果：
{"matches":[{"index":0,"reason":"中断原因或空字符串"},...]}

`)

	// 构建最近的日报索引：按时间排序后的日报
	type dailyIdx struct {
		idx  int
		rec  DailyRecord
		diff time.Duration
	}

	sb.WriteString("以下是需要匹配的月报记录列表（每条包含索引、小区名称、时间）：\n")
	for offset, mr := range batch {
		sb.WriteString(fmt.Sprintf("- 索引 %d: 小区=", batchStart+offset))
		sb.WriteString(mr.CellName)
		sb.WriteString(", 时间=")
		sb.WriteString(mr.OccurTimeStr)
		sb.WriteString("\n")
	}

	// 为每条月报记录寻找最近的日报候选
	sb.WriteString("\n以下是日报记录列表（供匹配参考）：\n")
	// 构建一个日报时间索引，只输出时间接近的记录
	for i, dr := range dailyRecords {
		sb.WriteString(fmt.Sprintf("  D%d: 小区号=%s, 时间=%s, 中断原因=%s\n",
			i, dr.CellID, dr.OccurTimeStr, dr.InterruptReason))
	}

	sb.WriteString("\n请返回 JSON 格式的匹配结果，包含每个索引对应的中断原因。如果某条记录无法匹配，对应的 reason 设为空字符串。")

	return []deepseekMessage{
		{Role: "system", Content: "你是一个数据匹配专家。请严格按照 JSON 格式返回结果，不要添加额外说明。"},
		{Role: "user", Content: sb.String()},
	}
}

// parseAIResponse 解析 Deepseek 返回的 JSON 结果
type aiMatchItem struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

type aiMatchResponse struct {
	Matches []aiMatchItem `json:"matches"`
}

// DeepseekEnhanceMatching 使用 Deepseek AI 进行增强匹配
func (a *App) DeepseekEnhanceMatching(monthlyPath, dailyPath string) ([]MatchResult, error) {
	if a.deepseekKey == "" {
		return nil, fmt.Errorf("请先设置 Deepseek API 密钥（点击「配置 API」按钮）")
	}

	// 先运行基础匹配获取结果
	a.emitProgress(0, 100, "正在读取数据文件...", "reading")
	monthlyRecords, err := a.readMonthlyReport(monthlyPath)
	if err != nil {
		return nil, fmt.Errorf("读取月报失败: %v", err)
	}
	dailyRecords, err := a.readDailyReport(dailyPath)
	if err != nil {
		return nil, fmt.Errorf("读取日报失败: %v", err)
	}
	if len(monthlyRecords) == 0 || len(dailyRecords) == 0 {
		return nil, fmt.Errorf("数据文件中无有效记录")
	}

	twelveHours := 12 * time.Hour
	totalMonthly := len(monthlyRecords)

	// 使用基础算法先跑一遍，收集结果和未匹配的记录
	a.emitProgress(0, totalMonthly, "正在运行基础匹配...", "matching")
	var results []MatchResult
	var unmatchedMonthly []MonthlyRecord

	for i, mr := range monthlyRecords {
		if i%20 == 0 {
			a.emitProgress(i+1, totalMonthly,
				fmt.Sprintf("基础匹配中 %d/%d...", i+1, totalMonthly), "matching")
		}

		var bestMatch *DailyRecord
		bestSimilarity := 0.0
		bestTimeDiff := time.Duration(0)
		cleanMonthly := a.CleanString(mr.CellName)

		for _, dr := range dailyRecords {
			// 步骤一：时间剪枝 — 保留时间差在 ±12h 内的记录
			timeDiff := mr.OccurTime.Sub(dr.OccurTime)
			if timeDiff < -twelveHours || timeDiff > twelveHours {
				continue
			}

			cleanDaily := a.CleanString(dr.CellID)
			if cleanMonthly == "" || cleanDaily == "" {
				continue
			}

			similarity := a.CalculateSimilarity(mr.CellName, dr.CellID)
			if similarity >= 0.65 && similarity > bestSimilarity {
				bestSimilarity = similarity
				bestMatch = &dr
				bestTimeDiff = timeDiff
			}
		}

		if bestMatch != nil {
			if i < 5 {
				fmt.Printf("[DEBUG-AI] ✓ 基础命中 | 月报='%s'→日报='%s' | 相似度=%.4f | 时间差=%v\n",
					mr.CellName, bestMatch.CellID, bestSimilarity, bestTimeDiff)
			}
			results = append(results, MatchResult{
				MonthlyCellName: mr.CellName,
				DailyCellID:     bestMatch.CellID,
				TimeDiff:        formatTimeDiff(bestTimeDiff),
				SimilarityScore: math.Round(bestSimilarity*10000) / 10000,
				InterruptReason: bestMatch.InterruptReason,
				AIMatched:       false,
			})
		} else {
			unmatchedMonthly = append(unmatchedMonthly, mr)
			if i < 5 {
				fmt.Printf("[DEBUG-AI] ✗ 基础未命中 | 月报='%s'(清洗='%s')\n",
					mr.CellName, cleanMonthly)
			}
		}
	}

	// 如果没有未匹配的记录，直接返回
	if len(unmatchedMonthly) == 0 {
		a.emitProgress(totalMonthly, totalMonthly, "全部已匹配，无需 AI 增强", "done")
		return results, nil
	}

	a.emitProgress(0, len(unmatchedMonthly),
		fmt.Sprintf("AI 增强匹配：还有 %d 条未匹配记录，正在调用 Deepseek...", len(unmatchedMonthly)),
		"ai-enhancing")

	// 分批调用 Deepseek API（每批 8 条）
	batchSize := 8
	totalUnmatched := len(unmatchedMonthly)
	aiMatched := 0

	for batchStart := 0; batchStart < totalUnmatched; batchStart += batchSize {
		end := batchStart + batchSize
		if end > totalUnmatched {
			end = totalUnmatched
		}

		a.emitProgress(batchStart+1, totalUnmatched,
			fmt.Sprintf("AI 分析中 %d/%d (第 %d 批)...", end, totalUnmatched, (batchStart/batchSize)+1),
			"ai-enhancing")

		prompt := buildAIPrompt(unmatchedMonthly, dailyRecords, batchStart, batchSize)
		aiResp, err := a.callDeepseekAPI(prompt)
		if err != nil {
			// AI 调用失败，跳过这批
			continue
		}

		// 解析 AI 返回的 JSON
		var matchResp aiMatchResponse
		if err := json.Unmarshal([]byte(aiResp), &matchResp); err != nil {
			// 尝试从 ```json 块中提取
			if idx := strings.Index(aiResp, "{"); idx >= 0 {
				if endIdx := strings.LastIndex(aiResp, "}"); endIdx > idx {
					json.Unmarshal([]byte(aiResp[idx:endIdx+1]), &matchResp)
				}
			}
		}

		for _, item := range matchResp.Matches {
			idx := item.Index
			reason := strings.TrimSpace(item.Reason)
			if idx < 0 || idx >= len(unmatchedMonthly) || reason == "" {
				continue
			}

			mr := unmatchedMonthly[idx]
			// 找到匹配的日报记录（暂不校验时间窗口）
			for _, dr := range dailyRecords {
				if dr.InterruptReason == reason {
					timeDiff := mr.OccurTime.Sub(dr.OccurTime)
					similarity := a.CalculateSimilarity(mr.CellName, dr.CellID)

					results = append(results, MatchResult{
						MonthlyCellName: mr.CellName,
						DailyCellID:     dr.CellID,
						TimeDiff:        formatTimeDiff(timeDiff),
						SimilarityScore: math.Round(similarity*10000) / 10000,
						InterruptReason: reason,
						AIMatched:       true,
					})
					aiMatched++
					break
				}
			}
		}
	}

	a.emitProgress(totalUnmatched, totalUnmatched,
		fmt.Sprintf("AI 增强完成！基础匹配 %d 条 + AI 补充 %d 条 = 共 %d 条",
			len(results)-aiMatched, aiMatched, len(results)), "done")

	return results, nil
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

// ExportResults 将匹配结果导出为 Excel 文件
func (a *App) ExportResults(results []MatchResult) (string, error) {
	if len(results) == 0 {
		return "", fmt.Errorf("没有匹配结果可以导出")
	}

	savePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "导出匹配结果",
		DefaultFilename: fmt.Sprintf("匹配结果_%s.xlsx", time.Now().Format("20060102_150405")),
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel 文件 (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("打开保存对话框失败: %v", err)
	}
	if savePath == "" {
		return "", nil
	}
	if !strings.HasSuffix(strings.ToLower(savePath), ".xlsx") {
		savePath += ".xlsx"
	}

	f := excelize.NewFile()
	defer f.Close()
	sheetName := "匹配结果"
	f.SetSheetName("Sheet1", sheetName)

	// 判断使用新格式还是旧格式
	if len(results) > 0 && len(results[0].RowAData) > 0 {
		// 新格式：A 表所有原始列 + 最后追加「匹配结果(由B表提取)」
		numACols := len(results[0].RowAData)
		colLetter := func(n int) string { c, _ := excelize.ColumnNumberToName(n + 1); return c }
		colNums := make([]int, numACols+1)
		for i := 0; i < numACols; i++ {
			colNums[i] = i
			f.SetCellValue(sheetName, fmt.Sprintf("%s1", colLetter(i)), fmt.Sprintf("A-Col%d", i+1))
		}
		extractCol := numACols
		colNums[numACols] = extractCol
		f.SetCellValue(sheetName, fmt.Sprintf("%s1", colLetter(extractCol)), "匹配结果(由B表提取)")

		headerStyle, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Bold: true, Size: 12, Color: "FFFFFF"},
			Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"4472C4"}},
		})
		f.SetCellStyle(sheetName, "A1", fmt.Sprintf("%s1", colLetter(extractCol)), headerStyle)

		for i, r := range results {
			rowNum := i + 2
			for _, ci := range colNums {
				if ci < numACols {
					f.SetCellValue(sheetName, fmt.Sprintf("%s%d", colLetter(ci), rowNum), r.RowAData[ci])
				} else {
					f.SetCellValue(sheetName, fmt.Sprintf("%s%d", colLetter(ci), rowNum), r.ExtractValue)
				}
			}
		}
		for ci := range colNums {
			f.SetColWidth(sheetName, colLetter(ci), colLetter(ci), 22)
		}
	} else {
		// 旧格式 向后兼容
		headers := []string{"月报小区名称", "日报小区号", "匹配时间差", "相似度得分", "统计到的中断原因", "AI辅助匹配"}
		for i, h := range headers {
			col, _ := excelize.ColumnNumberToName(i + 1)
			f.SetCellValue(sheetName, fmt.Sprintf("%s1", col), h)
		}
		headerStyle, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Bold: true, Size: 12, Color: "FFFFFF"},
			Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"4472C4"}},
		})
		lastCol, _ := excelize.ColumnNumberToName(len(headers))
		f.SetCellStyle(sheetName, "A1", fmt.Sprintf("%s1", lastCol), headerStyle)

		for i, r := range results {
			rowNum := i + 2
			f.SetCellValue(sheetName, fmt.Sprintf("A%d", rowNum), r.MonthlyCellName)
			f.SetCellValue(sheetName, fmt.Sprintf("B%d", rowNum), r.DailyCellID)
			f.SetCellValue(sheetName, fmt.Sprintf("C%d", rowNum), r.TimeDiff)
			f.SetCellValue(sheetName, fmt.Sprintf("D%d", rowNum), r.SimilarityScore)
			f.SetCellValue(sheetName, fmt.Sprintf("E%d", rowNum), r.InterruptReason)
			aiLabel := "否"
			if r.AIMatched {
				aiLabel = "是"
			}
			f.SetCellValue(sheetName, fmt.Sprintf("F%d", rowNum), aiLabel)
		}
		for _, c := range []string{"A", "B", "C", "D", "E", "F"} {
			f.SetColWidth(sheetName, c, c, 22)
		}
	}

	if err := f.SaveAs(savePath); err != nil {
		return "", fmt.Errorf("保存文件失败: %v", err)
	}
	return savePath, nil
}
