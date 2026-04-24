// Package proxy302 提供与 proxy302.com 住宅代理 API 的交互功能
// 基础域名: https://open.proxy302.com/open_api/v3/
package proxy302

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	baseURL  = "https://open.proxy302.com/open_api/v3"
	username = "kiro"
	password = "1iAa2eM8s8jhjYdYOy"
)

// httpClient 复用连接池
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// ==================== 响应结构体 ====================

// TokenResponse /user/users/token 接口响应
type TokenResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Token string `json:"token"`
	} `json:"data"`
}

// StaticTrafficData 静态流量代理信息
type StaticTrafficData struct {
	TokenID  int    `json:"token_id"`
	IP       string `json:"ip"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	UserName string `json:"user_name"`
	Password string `json:"password"`
	Protocol string `json:"protocol"`
}

// StaticTrafficResponse /proxy/api/proxy/static/traffic 接口响应
type StaticTrafficResponse struct {
	Code int               `json:"code"`
	Msg  string            `json:"msg"`
	Data StaticTrafficData `json:"data"`
}

// IPLocationData IP 地理位置信息
type IPLocationData struct {
	IP          string `json:"ip"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Region      string `json:"region"`
	City        string `json:"city"`
}

// GetIPLocation 查询 IP 的物理地址，依次尝试多个服务
func GetIPLocation(ip string) (*IPLocationData, error) {
	// 备用服务列表，依次尝试
	type fetcher func(string) (*IPLocationData, error)
	services := []fetcher{
		getIPLocationFromIPAPI,
		getIPLocationFromIPInfo,
	}

	var lastErr error
	for _, fn := range services {
		loc, err := fn(ip)
		if err == nil {
			return loc, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// getIPLocationFromIPAPI 使用 ip-api.com 查询
func getIPLocationFromIPAPI(ip string) (*IPLocationData, error) {
	url := fmt.Sprintf("https://ip-api.com/json/%s?fields=status,message,country,countryCode,regionName,city,query", ip)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ip-api.com parse error: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("ip-api.com failed: %s", result.Message)
	}

	return &IPLocationData{
		IP:          result.Query,
		Country:     result.Country,
		CountryCode: result.CountryCode,
		Region:      result.RegionName,
		City:        result.City,
	}, nil
}

// getIPLocationFromIPInfo 使用 ipinfo.io 查询（备用）
func getIPLocationFromIPInfo(ip string) (*IPLocationData, error) {
	url := fmt.Sprintf("https://ipinfo.io/%s/json", ip)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		IP      string `json:"ip"`
		City    string `json:"city"`
		Region  string `json:"region"`
		Country string `json:"country"`
		Org     string `json:"org"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ipinfo.io parse error: %w", err)
	}
	if result.IP == "" {
		return nil, fmt.Errorf("ipinfo.io: empty response")
	}

	return &IPLocationData{
		IP:          result.IP,
		Country:     result.Country,
		CountryCode: result.Country, // ipinfo 只返回 2 字母代码
		Region:      result.Region,
		City:        result.City,
	}, nil
}

// GetToken 获取 API Token
// GET /user/users/token?username=xxx&password=xxx
func GetToken() (string, error) {
	url := fmt.Sprintf("%s/user/users/token?username=%s&password=%s", baseURL, username, password)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("proxy302 GetToken: build request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("proxy302 GetToken: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("proxy302 GetToken: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("proxy302 GetToken: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result TokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("proxy302 GetToken: parse response: %w", err)
	}

	if result.Code != 0 {
		return "", fmt.Errorf("proxy302 GetToken: API error %d: %s", result.Code, result.Msg)
	}

	return result.Data.Token, nil
}

// GetStaticTrafficProxy 获取静态流量代理
// POST /proxy/api/proxy/static/traffic
// 参数固定：s=1, protocol=socks5, is_data_center=1, country_id=233, state_id=0, city_id=0
func GetStaticTrafficProxy(token string) (*StaticTrafficData, error) {
	url := fmt.Sprintf("%s/proxy/api/proxy/static/traffic?s=1&protocol=socks5&is_data_center=1&country_id=233&state_id=0&city_id=0", baseURL)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: build request: %w", err)
	}

	req.Header.Set("Authorization", token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result StaticTrafficResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: parse response: %w", err)
	}

	if result.Code != 0 {
		return nil, fmt.Errorf("proxy302 GetStaticTrafficProxy: API error %d: %s", result.Code, result.Msg)
	}

	// 固定使用 us.proxy302.com:3333
	result.Data.Host = "us.proxy302.com"
	result.Data.Port = 3333

	return &result.Data, nil
}
