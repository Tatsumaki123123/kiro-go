// AdsPower CORS Relay
// 监听 50326 端口，转发请求到本机 AdsPower (50325)，并添加 CORS 头
// 用法：直接双击运行，无需安装任何依赖
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	listenAddr  = ":50326"
	adsTarget   = "http://127.0.0.1:50325"
)

func main() {
	fmt.Println("╔════════════════════════════════════════╗")
	fmt.Println("║     AdsPower CORS Relay v1.0           ║")
	fmt.Println("║  转发 50326 → AdsPower 50325           ║")
	fmt.Println("╚════════════════════════════════════════╝")
	fmt.Printf("\n✓ 监听 http://127.0.0.1%s\n", listenAddr)
	fmt.Println("  保持此窗口运行，关闭后中继停止")
	fmt.Println()

	http.HandleFunc("/", handleRelay)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		log.Fatal("启动失败:", err)
	}
}

func handleRelay(w http.ResponseWriter, r *http.Request) {
	// 允许所有来源的跨域请求
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Password")

	// 处理预检请求
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}

	// 构建目标 URL
	target := adsTarget + r.URL.RequestURI()

	// 读取请求体
	var body io.Reader
	if r.Body != nil {
		body = r.Body
		defer r.Body.Close()
	}

	// 创建转发请求
	req, err := http.NewRequest(r.Method, target, body)
	if err != nil {
		http.Error(w, "Bad request: "+err.Error(), 400)
		return
	}

	// 透传 Content-Type 和 Authorization
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}

	// 发起请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") {
			http.Error(w, `{"code":-1,"msg":"AdsPower 未启动，请先打开 AdsPower"}`, 502)
		} else {
			http.Error(w, `{"code":-1,"msg":"`+err.Error()+`"}`, 502)
		}
		return
	}
	defer resp.Body.Close()

	// 透传响应
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Printf("[relay] %s %s → %d", r.Method, r.URL.Path, resp.StatusCode)
}
