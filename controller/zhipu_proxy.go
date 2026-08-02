package controller

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/logger"
)

var zhipuProxyUpstream = func() string {
	if v := os.Getenv("ZHIPU_PROXY_UPSTREAM"); v != "" {
		return v
	}
	return "https://open.bigmodel.cn/api/coding/paas/v4"
}()

var zhipuCaptureDir = func() string {
	dir := os.Getenv("ZHIPU_PROXY_CAPTURE_DIR")
	if dir == "" {
		if logger.LogDir != "" {
			dir = logger.LogDir
		} else {
			dir = "/tmp"
		}
	}
	return dir
}()

func writeCapture(req *http.Request, body []byte, resp *http.Response, respBody []byte, status int, method, url string) {
	var sb strings.Builder
	sb.WriteString("========== " + time.Now().Format("2006-01-02 15:04:05.000") + " ==========\n")
	sb.WriteString("REQ " + method + " " + url + "\n")
	for k, v := range req.Header {
		sb.WriteString("  HDR " + k + ": " + strings.Join(v, ", ") + "\n")
	}
	sb.WriteString("REQ BODY:\n" + string(body) + "\n")
	if resp != nil {
		sb.WriteString("RESP " + method + " " + url + " => " + resp.Status + "\n")
		for k, v := range resp.Header {
			sb.WriteString("  RHDR " + k + ": " + strings.Join(v, ", ") + "\n")
		}
	}
	sb.WriteString("RESP BODY:\n" + string(respBody) + "\n")
	sb.WriteString("==========================================================\n\n")
	f := filepath.Join(zhipuCaptureDir, "zhipu_proxy_capture.log")
	_ = os.MkdirAll(zhipuCaptureDir, 0755)
	fd, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer fd.Close()
	_, _ = fd.WriteString(sb.String())
}

// ZhipuProxy relays a request to the configured zhipu upstream, capturing the
// full request/response (headers + body) to a log file for analysis. It is
// intentionally unauthenticated so a local client (e.g. zcode) can point its
// base_url at http://127.0.0.1:3793/zhipu and we observe exactly what it sends.
func ZhipuProxy(c *gin.Context) {
	target := c.Param("target")
	upstream := strings.TrimSuffix(zhipuProxyUpstream, "/") + target

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	req, err := http.NewRequest(c.Request.Method, upstream, bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for k, v := range c.Request.Header {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Connection") {
			continue
		}
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeCapture(req, body, nil, []byte("UPSTREAM_ERROR: "+err.Error()), 502, c.Request.Method, upstream)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		respBody = []byte{}
	}
	writeCapture(req, body, resp, respBody, resp.StatusCode, c.Request.Method, upstream)

	for k, v := range resp.Header {
		for _, vv := range v {
			c.Header(k, vv)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(respBody)
}
