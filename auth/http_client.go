// Package auth 提供认证相关功能的 HTTP 客户端
package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 全局 HTTP 客户端，复用连接池
// 用于所有 auth 模块的 HTTP 请求
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,               // 最大空闲连接数
		MaxIdleConnsPerHost: 10,               // 每个 Host 最大空闲连接数
		IdleConnTimeout:     90 * time.Second, // 空闲连接超时
		DisableCompression:  false,            // 启用压缩
		ForceAttemptHTTP2:   true,             // 尝试使用 HTTP/2
	},
}

// 代理客户端缓存
var (
	proxyClientsMu sync.Mutex
	proxyClients   = make(map[string]*http.Client)
)

// GetHTTPClientWithProxy 根据代理 URL 返回 HTTP 客户端
func GetHTTPClientWithProxy(proxyURL string) *http.Client {
	if proxyURL == "" {
		return httpClient
	}

	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()

	if c, ok := proxyClients[proxyURL]; ok {
		return c
	}

	transport, err := buildProxyTransport(proxyURL)
	if err != nil {
		return httpClient
	}

	c := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
	proxyClients[proxyURL] = c
	return c
}

// buildProxyTransport 构建代理 Transport
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
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
}
