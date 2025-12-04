package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ProviderCustom = "custom"
)

var (
	DefaultTimeout = 120 * time.Second
)

// Client AI API配置
type Client struct {
	Provider   string
	APIKey     string
	BaseURL    string
	Model      string
	Timeout    time.Duration
	UseFullURL bool // 是否使用完整URL（不添加/chat/completions）
	MaxTokens  int  // AI响应的最大token数
}

func New() AIClient {
	// 从环境变量读取 MaxTokens，默认 2000
	maxTokens := 2000
	if envMaxTokens := os.Getenv("AI_MAX_TOKENS"); envMaxTokens != "" {
		if parsed, err := strconv.Atoi(envMaxTokens); err == nil && parsed > 0 {
			maxTokens = parsed
			log.Printf("🔧 [MCP] 使用环境变量 AI_MAX_TOKENS: %d", maxTokens)
		} else {
			log.Printf("⚠️  [MCP] 环境变量 AI_MAX_TOKENS 无效 (%s)，使用默认值: %d", envMaxTokens, maxTokens)
		}
	}

	// 默认配置
	return &Client{
		Provider:  ProviderDeepSeek,
		BaseURL:   DefaultDeepSeekBaseURL,
		Model:     DefaultDeepSeekModel,
		Timeout:   DefaultTimeout,
		MaxTokens: maxTokens,
	}
}

// SetCustomAPI 设置自定义OpenAI兼容API
func (client *Client) SetAPIKey(apiKey, apiURL, customModel string) {
	client.Provider = ProviderCustom
	client.APIKey = apiKey

	// 检查URL是否以#结尾，如果是则使用完整URL（不添加/chat/completions）
	if strings.HasSuffix(apiURL, "#") {
		client.BaseURL = strings.TrimSuffix(apiURL, "#")
		client.UseFullURL = true
	} else {
		client.BaseURL = apiURL
		client.UseFullURL = false
	}

	client.Model = customModel
	client.Timeout = 180 * time.Second
}

// CallWithMessages 使用 system + user prompt 调用AI API（推荐）
func (client *Client) CallWithMessages(systemPrompt, userPrompt string) (string, error) {
	if client.APIKey == "" {
		return "", fmt.Errorf("AI API密钥未设置，请先调用 SetAPIKey")
	}

	// Token 限制檢查（第一次調用時檢查）
	checkTokenLimits(systemPrompt, userPrompt, client.Model)

	// 重试配置
	maxRetries := 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			fmt.Printf("⚠️  AI API调用失败，正在重试 (%d/%d)...\n", attempt, maxRetries)
		}

		result, err := client.callOnce(systemPrompt, userPrompt)
		if err == nil {
			if attempt > 1 {
				fmt.Printf("✓ AI API重试成功\n")
			}
			return result, nil
		}

		lastErr = err
		// 如果不是网络错误，不重试
		if !isRetryableError(err) {
			return "", err
		}

		// 重试前等待
		if attempt < maxRetries {
			waitTime := time.Duration(attempt) * 2 * time.Second
			fmt.Printf("⏳ 等待%v后重试...\n", waitTime)
			time.Sleep(waitTime)
		}
	}

	return "", fmt.Errorf("重试%d次后仍然失败: %w", maxRetries, lastErr)
}

func (client *Client) setAuthHeader(reqHeader http.Header) {
	reqHeader.Set("Authorization", fmt.Sprintf("Bearer %s", client.APIKey))
}

// callOnce 单次调用AI API（内部使用）
func (client *Client) callOnce(systemPrompt, userPrompt string) (string, error) {
	// 打印当前 AI 配置
	log.Printf("📡 [MCP] AI 请求配置:")
	log.Printf("   Provider: %s", client.Provider)
	log.Printf("   BaseURL: %s", client.BaseURL)
	log.Printf("   Model: %s", client.Model)
	log.Printf("   UseFullURL: %v", client.UseFullURL)
	if len(client.APIKey) > 8 {
		log.Printf("   API Key: %s...%s", client.APIKey[:4], client.APIKey[len(client.APIKey)-4:])
	}

	// 构建 messages 数组
	messages := []map[string]string{}

	// 如果有 system prompt，添加 system message
	if systemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	// 添加 user message
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": userPrompt,
	})

	// 构建请求体
	requestBody := map[string]interface{}{
		"model":       client.Model,
		"messages":    messages,
		"temperature": 0.5, // 降低temperature以提高JSON格式稳定性
		"max_tokens":  client.MaxTokens,
	}

	// 注意：response_format 参数仅 OpenAI 支持，DeepSeek/Qwen 不支持
	// 我们通过强化 prompt 和后处理来确保 JSON 格式正确

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建HTTP请求
	var url string
	if client.UseFullURL {
		// 使用完整URL，不添加/chat/completions
		url = client.BaseURL
	} else {
		// 默认行为：添加/chat/completions
		url = fmt.Sprintf("%s/chat/completions", client.BaseURL)
	}
	log.Printf("📡 [MCP] 请求 URL: %s", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client.setAuthHeader(req.Header)

	// 发送请求
	httpClient := &http.Client{Timeout: client.Timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API返回错误 (status %d): %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API返回空响应")
	}

	return result.Choices[0].Message.Content, nil
}

// isRetryableError 判断错误是否可重试
func isRetryableError(err error) bool {
	errStr := err.Error()
	// 网络错误、超时、EOF等可以重试
	retryableErrors := []string{
		"EOF",
		"timeout",
		"connection reset",
		"connection refused",
		"temporary failure",
		"no such host",
		"stream error",   // HTTP/2 stream 错误
		"INTERNAL_ERROR", // 服务端内部错误
	}
	for _, retryable := range retryableErrors {
		if strings.Contains(errStr, retryable) {
			return true
		}
	}
	return false
}

// ModelLimits AI模型的token限制
type ModelLimits struct {
	SystemPromptLimit int // System prompt 最大 tokens
	TotalLimit        int // System + User prompt 總和限制
	Model             string
}

// getModelLimits 獲取指定模型的token限制
func getModelLimits(modelName string) ModelLimits {
	modelLower := strings.ToLower(modelName)

	// Qwen 系列
	if strings.Contains(modelLower, "qwen") {
		if strings.Contains(modelLower, "max") {
			// Qwen3-Max: 個人API Key限制較嚴格
			return ModelLimits{
				SystemPromptLimit: 8192,  // 個人版限制
				TotalLimit:        32768, // 總限制
				Model:             "Qwen3-Max (個人版)",
			}
		}
		return ModelLimits{
			SystemPromptLimit: 16000,
			TotalLimit:        32000,
			Model:             "Qwen",
		}
	}

	// DeepSeek 系列
	if strings.Contains(modelLower, "deepseek") {
		// DeepSeek-V3/V2: 128K context window
		if strings.Contains(modelLower, "v3") || strings.Contains(modelLower, "v2") {
			return ModelLimits{
				SystemPromptLimit: 100000, // 留28K buffer給輸出
				TotalLimit:        128000, // 128K context
				Model:             "DeepSeek-V3/V2",
			}
		}
		// deepseek-chat（舊版本）: 32K context
		return ModelLimits{
			SystemPromptLimit: 24000, // 留8K buffer給輸出
			TotalLimit:        32000, // 32K context
			Model:             "DeepSeek-Chat",
		}
	}

	// GPT 系列
	if strings.Contains(modelLower, "gpt-4") {
		if strings.Contains(modelLower, "turbo") || strings.Contains(modelLower, "128k") {
			return ModelLimits{
				SystemPromptLimit: 100000,
				TotalLimit:        128000,
				Model:             "GPT-4-Turbo",
			}
		}
		return ModelLimits{
			SystemPromptLimit: 8192,
			TotalLimit:        8192,
			Model:             "GPT-4",
		}
	}

	// 默認（保守估計）
	return ModelLimits{
		SystemPromptLimit: 8000,
		TotalLimit:        16000,
		Model:             "Unknown (保守估計)",
	}
}

// estimateTokens 粗略估算文本的token數量
// 估算規則：
//   - 中文：約1.5-2字符 = 1 token
//   - 英文：約4字符 = 1 token
//   - 混合文本：用2.5字符 = 1 token（保守估計）
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}

	// 計算字符數（Unicode字符）
	chars := utf8.RuneCountInString(text)

	// 粗略估算：2.5 字符 ≈ 1 token（保守估計）
	return chars / 2
}

// checkTokenLimits 檢查並警告token使用情況
func checkTokenLimits(systemPrompt, userPrompt, modelName string) {
	systemTokens := estimateTokens(systemPrompt)
	userTokens := estimateTokens(userPrompt)
	totalTokens := systemTokens + userTokens

	limits := getModelLimits(modelName)

	// 檢查 System Prompt 限制
	if systemTokens > limits.SystemPromptLimit {
		log.Println("")
		log.Println("╔═══════════════════════════════════════════════════════════════════╗")
		log.Printf("║  🚨 警告：System Prompt Token 超限！                              ║")
		log.Println("╟───────────────────────────────────────────────────────────────────╢")
		log.Printf("║  模型：%-58s║", limits.Model)
		log.Printf("║  System Prompt：%d tokens（限制：%d tokens）%-15s║",
			systemTokens, limits.SystemPromptLimit, "")
		log.Printf("║  超出：%d tokens (%.1f%%)%-41s║",
			systemTokens-limits.SystemPromptLimit,
			float64(systemTokens-limits.SystemPromptLimit)/float64(limits.SystemPromptLimit)*100, "")
		log.Println("║                                                                   ║")
		log.Println("║  ⚠️  預期影響：                                                   ║")
		log.Println("║    • Qwen3-Max: 會靜默截斷 User Prompt 尾部                      ║")
		log.Println("║    • 其他模型: 可能返回 400 錯誤或不完整響應                     ║")
		log.Println("║    • 關鍵交易數據可能丟失，導致錯誤決策                          ║")
		log.Println("║                                                                   ║")
		log.Println("║  🔧 解決方案：                                                    ║")
		log.Println("║    1. 切換到更小的 Prompt 模板（如 default.txt）                 ║")
		log.Println("║    2. 使用更大的模型（DeepSeek-V3 或 GPT-4-Turbo）              ║")
		log.Println("║    3. 聯繫管理員優化 Prompt 內容                                 ║")
		log.Println("╚═══════════════════════════════════════════════════════════════════╝")
		log.Println("")
	}

	// 檢查總 Token 限制
	if totalTokens > limits.TotalLimit {
		log.Println("")
		log.Println("╔═══════════════════════════════════════════════════════════════════╗")
		log.Printf("║  🔴 嚴重：總 Token 數超限！                                       ║")
		log.Println("╟───────────────────────────────────────────────────────────────────╢")
		log.Printf("║  模型：%-58s║", limits.Model)
		log.Printf("║  System Prompt：%d tokens%-40s║", systemTokens, "")
		log.Printf("║  User Prompt：  %d tokens%-40s║", userTokens, "")
		log.Printf("║  總計：%-10d tokens（限制：%d tokens）%-17s║",
			totalTokens, limits.TotalLimit, "")
		log.Printf("║  超出：%d tokens (%.1f%%)%-41s║",
			totalTokens-limits.TotalLimit,
			float64(totalTokens-limits.TotalLimit)/float64(limits.TotalLimit)*100, "")
		log.Println("║                                                                   ║")
		log.Println("║  ⚠️  這會導致：                                                   ║")
		log.Println("║    • API 靜默截斷數據（Qwen3-Max）                               ║")
		log.Println("║    • 候選幣種數據不完整                                           ║")
		log.Println("║    • AI 基於錯誤信息做決策                                        ║")
		log.Println("║    • 錯過交易機會或錯誤交易                                       ║")
		log.Println("║                                                                   ║")
		log.Println("║  🔧 緊急解決方案：                                                ║")
		log.Println("║    1. 減少候選幣種數量（AI500 或 OI_Top，不要同時開啟）         ║")
		log.Println("║    2. 切換到 DeepSeek-V3 (64K context window)                    ║")
		log.Println("║    3. 使用更小的 Prompt 模板                                      ║")
		log.Println("╚═══════════════════════════════════════════════════════════════════╝")
		log.Println("")
	} else if totalTokens > int(float64(limits.TotalLimit)*0.8) {
		// 接近限制（80%以上）時給予提示
		log.Printf("⚠️  [Token] 接近限制：System %d + User %d = %d tokens (限制: %d, 使用率: %.1f%%)",
			systemTokens, userTokens, totalTokens, limits.TotalLimit,
			float64(totalTokens)/float64(limits.TotalLimit)*100)
	} else {
		// 正常情況下也記錄，便於調試
		log.Printf("✓ [Token] System %d + User %d = %d tokens (限制: %d, 使用率: %.1f%%)",
			systemTokens, userTokens, totalTokens, limits.TotalLimit,
			float64(totalTokens)/float64(limits.TotalLimit)*100)
	}
}
