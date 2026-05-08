package service

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
)

// NewProbeHTTPClient 创建用于探测上游模型列表的 HTTP 客户端。
// 支持通过代理发起请求。
func NewProbeHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	return httpclient.GetClient(httpclient.Options{
		ProxyURL:          proxyURL,
		Timeout:           timeout,
		AllowPrivateHosts: true,
	})
}
