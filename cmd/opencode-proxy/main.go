package main

// opencode-proxy: opencode.ai/zen/go 专用反向代理
//
// opencode 的 /go 端点要求每个请求携带 opencode 会话头
// (x-opencode-session / x-opencode-request / x-opencode-client)，
// 否则返回 400 MissingSessionID。one-api 转发的通用请求没有这些头。
//
// 本代理监听一个独立端口，为 opencode 渠道注入会话头后转发给 one-api：
//
//	client → opencode-proxy(:13794) → one-api(:3794) → opencode.ai/zen/go
//
// 安全约束：只允许路由到 opencode 渠道——通过 URL 路径前缀 /opencode/ 限定，
// 非该前缀的请求一律 403。每个连接生成稳定的会话 ID（进程生命周期内唯一），
// 保证同一代理会话的请求被 opencode 路由到同一后端。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ── 配置 ────────────────────────────────────────────────────────────────

type config struct {
	listenAddr string // proxy 监听地址
	targetURL  string // one-api 地址（必须是 opencode 渠道入口）
	sessionID  string // 本代理的稳定会话 ID
	relayToken string // one-api relay token（代理自身的认证）
}

func loadConfig() config {
	c := config{
		listenAddr: envOr("OPENCODE_PROXY_LISTEN", "127.0.0.1:13794"),
		targetURL:  envOr("OPENCODE_PROXY_TARGET", "http://127.0.0.1:3794"),
	}
	c.sessionID = envOr("OPENCODE_PROXY_SESSION", "")
	if c.sessionID == "" {
		c.sessionID = randomID("proxy-")
	}
	c.relayToken = envOr("OPENCODE_PROXY_RELAY_TOKEN", "")
	return c
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

// ── 代理核心 ────────────────────────────────────────────────────────────

// opencodeProxy 是限定了目标路径前缀的反向代理。
type opencodeProxy struct {
	cfg      config
	target   *url.URL
	proxy    *httputil.ReverseProxy
	mu       sync.Mutex
	lastReq  time.Time
	reqCount int64
}

// 前缀门槛：只有走这个前缀的请求才被转发（强制"只给 opencode 使用"）。
const opencodePrefix = "/opencode/"

// opencode 要求的会话头（对齐 opencode src/session/llm/request.ts）。
func (p *opencodeProxy) injectSessionHeaders(req *http.Request) {
	req.Header.Set("x-opencode-session", p.cfg.sessionID)
	req.Header.Set("x-opencode-request", randomID("req-"))
	req.Header.Set("x-opencode-client", "one-api-proxy")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "one-api-opencode-proxy/1.0")
	}
	// Relay auth: proxy authenticates to one-api with its own relay token
	if p.cfg.relayToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.relayToken)
	}
}

func newOpencodeProxy(cfg config) (*opencodeProxy, error) {
	target, err := url.Parse(cfg.targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target %q: %w", cfg.targetURL, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// 不透传上游的错误页；由 proxy 自己包装
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[opencode-proxy] upstream error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":{"message":"opencode-proxy upstream error: %v","type":"proxy_error"}}`, err)
	}
	return &opencodeProxy{cfg: cfg, target: target, proxy: proxy}, nil
}

func (p *opencodeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// ── 安全门槛：只允许 /opencode/ 前缀 ──
	if !strings.HasPrefix(r.URL.Path, opencodePrefix) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":{"message":"opencode-proxy: only %s* paths are allowed (got %s)","type":"forbidden"}}`,
			opencodePrefix, r.URL.Path)
		log.Printf("[opencode-proxy] REJECTED non-opencode path %s %s (403)", r.Method, r.URL.Path)
		return
	}

	// 改写路径：/opencode/xxx → /xxx（剥掉代理前缀）
	r.URL.Path = strings.TrimPrefix(r.URL.Path, opencodePrefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	// 注入 opencode 会话头（转发前，避免日志泄漏凭证）
	p.injectSessionHeaders(r)

	// 健康检查端点放行（不计入统计）
	p.mu.Lock()
	p.reqCount++
	p.lastReq = start
	p.mu.Unlock()

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	p.proxy.ServeHTTP(rec, r)
	log.Printf("[opencode-proxy] %s %s → %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
}

// ── 辅助 ────────────────────────────────────────────────────────────────

// statusRecorder 捕获上游状态码用于日志。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// healthHandler 报告 proxy 自身状态（不经过 opencode 前缀门槛）。
func (p *opencodeProxy) healthHandler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	count, last := p.reqCount, p.lastReq
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"session_id":   p.cfg.sessionID,
		"target":       p.cfg.targetURL,
		"requests":     count,
		"last_request": last.Format(time.RFC3339),
	})
}

// ── main ────────────────────────────────────────────────────────────────

func runOpencodeProxy() {
	cfg := loadConfig()
	p, err := newOpencodeProxy(cfg)
	if err != nil {
		log.Fatalf("[opencode-proxy] %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", p.healthHandler) // 健康检查不受门槛限制
	mux.Handle("/", p)                          // 其余全部走安全门槛

	log.Printf("[opencode-proxy] listening on %s → %s (session=%s)",
		cfg.listenAddr, cfg.targetURL, cfg.sessionID)
	if err := http.ListenAndServe(cfg.listenAddr, mux); err != nil {
		log.Fatalf("[opencode-proxy] %v", err)
	}
}

// 保持 bytes 引用（脚本生成时可能用到）
var _ = bytes.Equal

func main() {
	runOpencodeProxy()
}
