// Package proxy Kiro API 代理核心
// 负责调用 Kiro API 并解析 AWS Event Stream 响应
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kiro-api-proxy/config"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const KiroVersion = "0.7.45"

// 双端点配置（429 时自动 fallback）
type kiroEndpoint struct {
	URL       string
	Origin    string
	AmzTarget string
	Name      string
}

var kiroEndpoints = []kiroEndpoint{
	{
		URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		Origin:    "AI_EDITOR",
		AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		Name:      "CodeWhisperer",
	},
	{
		URL:       "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
		Origin:    "CLI",
		AmzTarget: "AmazonQDeveloperStreamingService.SendMessage",
		Name:      "AmazonQ",
	},
}

// 全局 HTTP 客户端，复用连接池
var kiroHttpClient = &http.Client{
	Timeout: 5 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        100,              // 最大空闲连接数
		MaxIdleConnsPerHost: 20,               // 每个 Host 最大空闲连接数
		IdleConnTimeout:     90 * time.Second, // 空闲连接超时
		DisableCompression:  false,            // 启用压缩
		ForceAttemptHTTP2:   true,             // 尝试使用 HTTP/2
	},
}

// 代理客户端缓存，key 为 proxyURL
var (
	proxyClientsMu sync.Mutex
	proxyClients   = make(map[string]*http.Client)
)

// getHTTPClient 根据账号的 ProxyURL 返回合适的 HTTP 客户端
func getHTTPClient(account *config.Account) *http.Client {
	log.Printf("[DEBUG] getHTTPClient called for account: %s, proxyURL: %q\n", account.Email, account.ProxyURL)
	
	if account.ProxyURL == "" {
		log.Printf("[DEBUG] No proxy configured, using default client\n")
		return kiroHttpClient
	}

	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()

	if c, ok := proxyClients[account.ProxyURL]; ok {
		log.Printf("[KiroAPI] Using cached proxy client %q for account %s\n", account.ProxyURL, account.Email)
		return c
	}

	log.Printf("[DEBUG] Creating new proxy client for %q\n", account.ProxyURL)
	transport, err := buildProxyTransport(account.ProxyURL)
	if err != nil {
		log.Printf("[KiroAPI] Invalid proxy URL %q: %v, using default client\n", account.ProxyURL, err)
		return kiroHttpClient
	}

	c := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: transport,
	}
	proxyClients[account.ProxyURL] = c
	log.Printf("[KiroAPI] ✓ Created proxy client for %q (account: %s)\n", account.ProxyURL, account.Email)
	
	// 检测代理出口 IP
	go detectProxyIP(c, account.ProxyURL, account.Email)
	
	return c
}

// buildProxyTransport 根据代理 URL 构建 http.Transport
// 支持 http/https 代理和 socks5 代理（通过环境变量方式）
func buildProxyTransport(proxyURL string) (http.RoundTripper, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return &http.Transport{
			Proxy:               http.ProxyURL(parsed),
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
}

// InvalidateProxyClient 当账号代理 URL 变更时，清除缓存的客户端
func InvalidateProxyClient(proxyURL string) {
	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()
	delete(proxyClients, proxyURL)
}

// detectProxyIP 检测代理的出口 IP 地址
func detectProxyIP(client *http.Client, proxyURL, accountEmail string) {
	// 使用多个 IP 检测服务，提高成功率
	ipServices := []string{
		"https://api.ipify.org?format=json",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
		"https://api.ip.sb/ip",
	}

	for _, service := range ipServices {
		req, err := http.NewRequest("GET", service, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "curl/7.68.0")

		// 设置较短的超时时间
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		ip := strings.TrimSpace(string(body))
		
		// 如果是 JSON 格式，解析出 IP
		if strings.HasPrefix(ip, "{") {
			var result struct {
				IP string `json:"ip"`
			}
			if json.Unmarshal(body, &result) == nil {
				ip = result.IP
			}
		}

		// 验证 IP 格式
		if ip != "" && (strings.Contains(ip, ".") || strings.Contains(ip, ":")) {
			log.Printf("[Proxy] ✓ Proxy %q exit IP: %s (account: %s)\n", proxyURL, ip, accountEmail)
			return
		}
	}

	log.Printf("[Proxy] ✗ Failed to detect exit IP for proxy %q (account: %s)\n", proxyURL, accountEmail)
}

// ==================== 请求结构 ====================

// KiroPayload Kiro API 请求体
type KiroPayload struct {
	ConversationState struct {
		ChatTriggerType string `json:"chatTriggerType"`
		ConversationID  string `json:"conversationId"`
		CurrentMessage  struct {
			UserInputMessage KiroUserInputMessage `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []KiroHistoryMessage `json:"history,omitempty"`
	} `json:"conversationState"`
	ProfileArn      string           `json:"profileArn,omitempty"`
	InferenceConfig *InferenceConfig `json:"inferenceConfig,omitempty"`
}

type KiroUserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId,omitempty"`
	Origin                  string                   `json:"origin"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

type UserInputMessageContext struct {
	Tools       []KiroToolWrapper `json:"tools,omitempty"`
	ToolResults []KiroToolResult  `json:"toolResults,omitempty"`
}

type KiroToolWrapper struct {
	ToolSpecification struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		InputSchema InputSchema `json:"inputSchema"`
	} `json:"toolSpecification"`
}

type InputSchema struct {
	JSON interface{} `json:"json"`
}

type KiroToolResult struct {
	ToolUseID string              `json:"toolUseId"`
	Content   []KiroResultContent `json:"content"`
	Status    string              `json:"status"`
}

type KiroResultContent struct {
	Text string `json:"text"`
}

type KiroImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type KiroHistoryMessage struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

type KiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

type KiroToolUse struct {
	ToolUseID string                 `json:"toolUseId"`
	Name      string                 `json:"name"`
	Input     map[string]interface{} `json:"input"`
}

type InferenceConfig struct {
	MaxTokens   int     `json:"maxTokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"topP,omitempty"`
}

// ==================== 错误类型 ====================

// ErrAccountSuspended 账号被暂停，调用方应禁用该账号并换号重试
type ErrAccountSuspended struct {
	UserID  string
	Message string
}

func (e *ErrAccountSuspended) Error() string {
	return fmt.Sprintf("account suspended (userId=%s): %s", e.UserID, e.Message)
}

// ==================== 流式回调 ====================

// KiroStreamCallback 流式响应回调
type KiroStreamCallback struct {
	OnText     func(text string, isThinking bool)
	OnToolUse  func(toolUse KiroToolUse)
	OnComplete func(inputTokens, outputTokens int)
	OnError    func(err error)
	OnCredits  func(credits float64)
}

// ==================== API 调用 ====================

// getSortedEndpoints 根据首选端点配置排序端点列表
func getSortedEndpoints(preferred string) []kiroEndpoint {
	if preferred == "amazonq" {
		return []kiroEndpoint{kiroEndpoints[1], kiroEndpoints[0]}
	}
	if preferred == "codewhisperer" {
		return []kiroEndpoint{kiroEndpoints[0], kiroEndpoints[1]}
	}
	// "auto" 或空值：默认顺序
	return []kiroEndpoint{kiroEndpoints[0], kiroEndpoints[1]}
}

// CallKiroAPI 调用 Kiro API（流式），双端点自动 fallback
func CallKiroAPI(account *config.Account, payload *KiroPayload, callback *KiroStreamCallback) error {
	if _, err := json.Marshal(payload); err != nil {
		return err
	}

	// User-Agent
	machineId := account.MachineId
	var userAgent, amzUserAgent string
	if machineId != "" {
		userAgent = fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", KiroVersion, machineId)
		amzUserAgent = fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE %s %s", KiroVersion, machineId)
	} else {
		userAgent = fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s", KiroVersion)
		amzUserAgent = fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE %s", KiroVersion)
	}

	// 根据配置排序端点
	endpoints := getSortedEndpoints(config.GetPreferredEndpoint())

	var lastErr error
	for _, ep := range endpoints {
		// 更新 payload 中的 origin
		payload.ConversationState.CurrentMessage.UserInputMessage.Origin = ep.Origin

		reqBody, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", ep.URL, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("X-Amz-Target", ep.AmzTarget)
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Amz-User-Agent", amzUserAgent)
		req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
		req.Header.Set("x-amzn-codewhisperer-optout", "true")
		req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
		req.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
		req.Header.Set("Authorization", "Bearer "+account.AccessToken)

		client := getHTTPClient(account)
		if account.ProxyURL != "" {
			log.Printf("[KiroAPI] → Sending request via proxy %q (account: %s, endpoint: %s)\n", 
				account.ProxyURL, account.Email, ep.Name)
		}
		
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[KiroAPI] Endpoint %s failed: %v\n", ep.Name, err)
			continue
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			log.Printf("[KiroAPI] Endpoint %s quota exhausted (429), trying next...\n", ep.Name)
			lastErr = fmt.Errorf("quota exhausted on %s", ep.Name)
			continue
		}

		if resp.StatusCode != 200 {
			errBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, ep.Name, string(errBody))

			// 检测账号暂停
			if resp.StatusCode == 403 {
				var errResp struct {
					Message string `json:"message"`
					Reason  string `json:"reason"`
				}
				if json.Unmarshal(errBody, &errResp) == nil && errResp.Reason == "TEMPORARILY_SUSPENDED" {
					// 提取 userId
					userID := ""
					if idx := strings.Index(errResp.Message, "Your User ID ("); idx != -1 {
						rest := errResp.Message[idx+14:]
						if end := strings.Index(rest, ")"); end != -1 {
							userID = rest[:end]
						}
					}
					return &ErrAccountSuspended{UserID: userID, Message: errResp.Message}
				}
			}

			// 其他认证错误不继续尝试
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return lastErr
			}
			log.Printf("[KiroAPI] Endpoint %s error: %v\n", ep.Name, lastErr)
			continue
		}

		err = parseEventStream(resp.Body, callback)
		resp.Body.Close()
		return err
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("all endpoints failed")
}

// ==================== Event Stream 解析 ====================

// parseEventStream 解析 AWS Event Stream 二进制格式
func parseEventStream(body io.Reader, callback *KiroStreamCallback) error {
	// 不使用 bufio，直接读取避免缓冲延迟
	var inputTokens, outputTokens int
	var totalCredits float64
	var currentToolUse *toolUseState
	var lastAssistantContent string
	var lastReasoningContent string

	for {
		// Prelude: 12 bytes (total_len + headers_len + crc)
		prelude := make([]byte, 12)
		_, err := io.ReadFull(body, prelude)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		totalLength := int(prelude[0])<<24 | int(prelude[1])<<16 | int(prelude[2])<<8 | int(prelude[3])
		headersLength := int(prelude[4])<<24 | int(prelude[5])<<16 | int(prelude[6])<<8 | int(prelude[7])

		if totalLength < 16 {
			continue
		}

		// 读取剩余部分
		remaining := totalLength - 12
		msgBuf := make([]byte, remaining)
		_, err = io.ReadFull(body, msgBuf)
		if err != nil {
			return err
		}

		if headersLength > len(msgBuf)-4 {
			continue
		}

		eventType := extractEventType(msgBuf[0:headersLength])
		payloadBytes := msgBuf[headersLength : len(msgBuf)-4]
		if len(payloadBytes) == 0 {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal(payloadBytes, &event); err != nil {
			continue
		}

		inputTokens, outputTokens = updateTokensFromEvent(event, inputTokens, outputTokens)

		// 处理事件
		switch eventType {
		case "assistantResponseEvent":
			if content, ok := event["content"].(string); ok && content != "" {
				normalized := normalizeChunk(content, &lastAssistantContent)
				if normalized != "" {
					callback.OnText(normalized, false)
				}
			}
		case "reasoningContentEvent":
			if text, ok := event["text"].(string); ok && text != "" {
				normalized := normalizeChunk(text, &lastReasoningContent)
				if normalized != "" {
					callback.OnText(normalized, true)
				}
			}
		case "toolUseEvent":
			currentToolUse = handleToolUseEvent(event, currentToolUse, callback)
		case "meteringEvent":
			if usage, ok := event["usage"].(float64); ok {
				totalCredits += usage
			}
		}
	}

	if callback.OnCredits != nil && totalCredits > 0 {
		callback.OnCredits(totalCredits)
	}

	callback.OnComplete(inputTokens, outputTokens)
	return nil
}

func updateTokensFromEvent(event map[string]interface{}, currentInputTokens, currentOutputTokens int) (int, int) {
	candidates := []map[string]interface{}{event}
	collectUsageMaps(event, &candidates)

	inputTokens := currentInputTokens
	outputTokens := currentOutputTokens

	for _, usage := range candidates {
		if usage == nil {
			continue
		}

		if v, ok := readTokenNumber(usage,
			"outputTokens", "completionTokens", "totalOutputTokens",
			"output_tokens", "completion_tokens", "total_output_tokens",
		); ok {
			outputTokens = v
		}

		if v, ok := readTokenNumber(usage,
			"inputTokens", "promptTokens", "totalInputTokens",
			"input_tokens", "prompt_tokens", "total_input_tokens",
		); ok {
			inputTokens = v
			continue
		}

		uncached, _ := readTokenNumber(usage, "uncachedInputTokens", "uncached_input_tokens")
		cacheRead, _ := readTokenNumber(usage, "cacheReadInputTokens", "cache_read_input_tokens")
		cacheWrite, _ := readTokenNumber(usage, "cacheWriteInputTokens", "cache_write_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens")
		if uncached+cacheRead+cacheWrite > 0 {
			inputTokens = uncached + cacheRead + cacheWrite
			continue
		}

		total, ok := readTokenNumber(usage, "totalTokens", "total_tokens")
		if ok && total > 0 {
			candidateOutput := outputTokens
			if v, vok := readTokenNumber(usage,
				"outputTokens", "completionTokens", "totalOutputTokens",
				"output_tokens", "completion_tokens", "total_output_tokens",
			); vok {
				candidateOutput = v
			}
			if total-candidateOutput > 0 {
				inputTokens = total - candidateOutput
			}
		}
	}

	return inputTokens, outputTokens
}

func collectUsageMaps(v interface{}, out *[]map[string]interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			lk := strings.ToLower(k)
			if lk == "usage" || lk == "tokenusage" || lk == "token_usage" {
				if m, ok := child.(map[string]interface{}); ok {
					*out = append(*out, m)
				}
			}
			collectUsageMaps(child, out)
		}
	case []interface{}:
		for _, child := range t {
			collectUsageMaps(child, out)
		}
	}
}

func normalizeChunk(chunk string, previous *string) string {
	if chunk == "" {
		return ""
	}

	prev := *previous
	if prev == "" {
		*previous = chunk
		return chunk
	}

	if chunk == prev {
		return ""
	}

	if strings.HasPrefix(chunk, prev) {
		delta := chunk[len(prev):]
		*previous = chunk
		return delta
	}

	if strings.HasPrefix(prev, chunk) {
		return ""
	}

	maxOverlap := 0
	maxLen := len(prev)
	if len(chunk) < maxLen {
		maxLen = len(chunk)
	}
	for i := maxLen; i > 0; i-- {
		if strings.HasSuffix(prev, chunk[:i]) {
			maxOverlap = i
			break
		}
	}

	*previous = chunk
	if maxOverlap > 0 {
		return chunk[maxOverlap:]
	}

	return chunk
}

func readTokenNumber(m map[string]interface{}, keys ...string) (int, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch n := v.(type) {
		case float64:
			return int(n), true
		case int:
			return n, true
		case int64:
			return int(n), true
		case json.Number:
			if parsed, err := n.Int64(); err == nil {
				return int(parsed), true
			}
		case string:
			if parsed, err := strconv.Atoi(n); err == nil {
				return parsed, true
			}
			if parsed, err := strconv.ParseFloat(n, 64); err == nil {
				return int(parsed), true
			}
		}
	}
	return 0, false
}

// ==================== Tool Use 处理 ====================

type toolUseState struct {
	ToolUseID   string
	Name        string
	InputBuffer strings.Builder
}

func handleToolUseEvent(event map[string]interface{}, current *toolUseState, callback *KiroStreamCallback) *toolUseState {
	toolUseID, _ := event["toolUseId"].(string)
	name, _ := event["name"].(string)
	isStop, _ := event["stop"].(bool)

	if toolUseID != "" && name != "" {
		if current == nil {
			current = &toolUseState{ToolUseID: toolUseID, Name: name}
		} else if current.ToolUseID != toolUseID {
			finishToolUse(current, callback)
			current = &toolUseState{ToolUseID: toolUseID, Name: name}
		}
	}

	if current != nil {
		if input, ok := event["input"].(string); ok {
			current.InputBuffer.WriteString(input)
		} else if inputObj, ok := event["input"].(map[string]interface{}); ok {
			data, _ := json.Marshal(inputObj)
			current.InputBuffer.Reset()
			current.InputBuffer.Write(data)
		}
	}

	if isStop && current != nil {
		finishToolUse(current, callback)
		return nil
	}

	return current
}

func finishToolUse(state *toolUseState, callback *KiroStreamCallback) {
	var input map[string]interface{}
	if state.InputBuffer.Len() > 0 {
		json.Unmarshal([]byte(state.InputBuffer.String()), &input)
	}
	if input == nil {
		input = make(map[string]interface{})
	}
	callback.OnToolUse(KiroToolUse{
		ToolUseID: state.ToolUseID,
		Name:      state.Name,
		Input:     input,
	})
}

// extractEventType 从 headers 中提取事件类型
func extractEventType(headers []byte) string {
	offset := 0
	for offset < len(headers) {
		if offset >= len(headers) {
			break
		}
		nameLen := int(headers[offset])
		offset++
		if offset+nameLen > len(headers) {
			break
		}
		name := string(headers[offset : offset+nameLen])
		offset += nameLen
		if offset >= len(headers) {
			break
		}
		valueType := headers[offset]
		offset++

		if valueType == 7 { // String
			if offset+2 > len(headers) {
				break
			}
			valueLen := int(headers[offset])<<8 | int(headers[offset+1])
			offset += 2
			if offset+valueLen > len(headers) {
				break
			}
			value := string(headers[offset : offset+valueLen])
			offset += valueLen
			if name == ":event-type" {
				return value
			}
			continue
		}

		// 跳过其他类型
		skipSizes := map[byte]int{0: 0, 1: 0, 2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16}
		if valueType == 6 {
			if offset+2 > len(headers) {
				break
			}
			l := int(headers[offset])<<8 | int(headers[offset+1])
			offset += 2 + l
		} else if skip, ok := skipSizes[valueType]; ok {
			offset += skip
		} else {
			break
		}
	}
	return ""
}
