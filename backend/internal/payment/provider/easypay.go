// Package provider contains concrete payment provider implementations.
package provider

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// EasyPay constants.
const (
	easypayCodeSuccess        = 200
	easypayCodeSuccessLegacy  = 1
	easypayStatusPaid         = 1
	easypayHTTPTimeout        = 10 * time.Second
	maxEasypayResponseSize    = 1 << 20 // 1MB
	maxEasypayErrorSummary    = 512
	tradeStatusSuccess        = "TRADE_SUCCESS"
	signTypeMD5               = "MD5"
	paymentModePopup          = "popup"
	easypayQueryModeFindOrder = "findorder"
	easypayFindOrderNoType    = "1"
	deviceMobile              = "mobile"
)

// EasyPay implements payment.Provider for the EasyPay aggregation platform.
type EasyPay struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

type easyPayCustomMethod struct {
	Type         string `json:"type"`
	UpstreamType string `json:"upstreamType"`
	DisplayName  string `json:"displayName"`
}

// NewEasyPay creates a new EasyPay provider.
// config keys: pid, pkey, apiBase, notifyUrl, returnUrl, cid, cidAlipay, cidWxpay,
// siteName/sitename, queryMode
func NewEasyPay(instanceID string, config map[string]string) (*EasyPay, error) {
	for _, k := range []string{"pid", "pkey", "apiBase", "notifyUrl", "returnUrl"} {
		if strings.TrimSpace(config[k]) == "" {
			return nil, fmt.Errorf("easypay config missing required key: %s", k)
		}
	}
	cfg := make(map[string]string, len(config))
	for k, v := range config {
		cfg[k] = v
	}
	cfg["apiBase"] = normalizeEasyPayAPIBase(cfg["apiBase"])
	return &EasyPay{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{Timeout: easypayHTTPTimeout},
	}, nil
}

func normalizeEasyPayAPIBase(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return ""
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.RawPath = ""
		parsed.Path = trimEasyPayEndpointPath(parsed.Path)
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimRight(trimEasyPayEndpointPath(base), "/")
}

func trimEasyPayEndpointPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	lower := strings.ToLower(path)
	for _, endpoint := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		if strings.HasSuffix(lower, endpoint) {
			return strings.TrimRight(path[:len(path)-len(endpoint)], "/")
		}
	}
	return path
}

func (e *EasyPay) apiBase() string {
	if e == nil {
		return ""
	}
	return normalizeEasyPayAPIBase(e.config["apiBase"])
}

func (e *EasyPay) Name() string        { return "EasyPay" }
func (e *EasyPay) ProviderKey() string { return payment.TypeEasyPay }
func (e *EasyPay) SupportedTypes() []payment.PaymentType {
	types := []payment.PaymentType{payment.TypeAlipay, payment.TypeWxpay}
	for _, method := range e.customMethods() {
		if method.Type != "" {
			types = append(types, method.Type)
		}
	}
	return types
}

func (e *EasyPay) MerchantIdentityMetadata() map[string]string {
	if e == nil {
		return nil
	}
	pid := strings.TrimSpace(e.config["pid"])
	if pid == "" {
		return nil
	}
	return map[string]string{"pid": pid}
}

func (e *EasyPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	// Payment mode determined by instance config, not payment type.
	// "popup" → hosted page (submit.php); "qrcode"/default → API call (mapi.php).
	mode := e.config["paymentMode"]
	if mode == paymentModePopup {
		return e.createRedirectPayment(req)
	}
	return e.createAPIPayment(ctx, req)
}

// createRedirectPayment builds a submit.php URL for browser redirect.
// No server-side API call — the user is redirected to EasyPay's hosted page.
// TradeNo is empty; it arrives via the notify callback after payment.
func (e *EasyPay) createRedirectPayment(req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount,
	}
	if siteName := e.resolveSiteName(); siteName != "" {
		params["sitename"] = siteName
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	payURL := e.apiBase() + "/submit.php?" + q.Encode()
	return &payment.CreatePaymentResponse{PayURL: payURL}, nil
}

// createAPIPayment calls mapi.php to get payurl/qrcode (existing behavior).
func (e *EasyPay) createAPIPayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount, "clientip": req.ClientIP,
	}
	if siteName := e.resolveSiteName(); siteName != "" {
		params["sitename"] = siteName
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	body, err := e.post(ctx, e.apiBase()+"/mapi.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay create: %w", err)
	}
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		TradeNo string `json:"trade_no"`
		PayURL  string `json:"payurl"`
		PayURL2 string `json:"payurl2"` // H5 mobile payment URL
		QRCode  string `json:"qrcode"`
		CodeURL string `json:"code_url"` // ezfpy-compatible QR image/content URL
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay parse: %w", err)
	}
	if !easyPaySuccessCode(resp.Code) {
		return nil, fmt.Errorf("easypay error: %s", resp.Msg)
	}
	payURL := resp.PayURL
	if req.IsMobile && resp.PayURL2 != "" {
		payURL = resp.PayURL2
	}
	qrCode := resp.QRCode
	if qrCode == "" {
		qrCode = resp.CodeURL
	}
	base := e.apiBase()
	return &payment.CreatePaymentResponse{
		TradeNo: resp.TradeNo,
		PayURL:  resolveEasyPayReturnedRef(base, payURL),
		QRCode:  resolveEasyPayReturnedRef(base, qrCode),
	}, nil
}

// resolveEasyPayReturnedRef absolutizes a payurl/payurl2/qrcode reference that
// mapi.php returned, resolving it against the instance's configured apiBase.
//
// EasyPay-compatible upstreams disagree on this field: some answer with a full
// checkout URL, others with a site-root-relative path such as
// "/api/pay/toapp/<order>". createRedirectPayment already builds an absolute URL
// itself (apiBase + "/submit.php?..."), but the mapi.php branch stored whatever
// came back verbatim, and nothing downstream repairs it —
// sanitizeCreatePaymentResponseDetails only strips NUL bytes before the value is
// persisted to pay_url/qr_code. The frontend then feeds qr_code straight into
// QRCode.toCanvas (PaymentQRCodeView.renderQR), so a relative path becomes a QR
// whose payload is a bare path: WeChat renders it as text, and pay_url resolves
// against the gateway's own domain and 404s.
//
// That is precisely the outcome the Alipay provider already refuses to produce —
// "Setting it as QRCode would let the frontend render an unscannable image"
// (createPagePayTrade) — so EasyPay is brought in line with the same rule.
//
// Only references beginning with "/" are resolved. Anything carrying a scheme is
// already actionable and is returned untouched, which covers both absolute
// https:// checkout URLs and app deep links (weixin://, wxp://, alipays://). A
// reference without a leading slash is left alone as well: QR payloads are
// frequently opaque tokens rather than paths, and rewriting "OrderToken123" into
// "<apiBase>/OrderToken123" would break a payload that works today — strictly
// worse than the bug this fixes.
func resolveEasyPayReturnedRef(apiBase, ref string) string {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(trimmed, "/") {
		return ref
	}
	base, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ref
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "" {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

// resolveURLs returns (notifyURL, returnURL) preferring request values,
// falling back to instance config.
func (e *EasyPay) resolveURLs(req payment.CreatePaymentRequest) (string, string) {
	notifyURL := req.NotifyURL
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	return notifyURL, returnURL
}

func (e *EasyPay) customMethods() []easyPayCustomMethod {
	if e == nil {
		return nil
	}
	raw := strings.TrimSpace(e.config["customMethods"])
	if raw == "" {
		return nil
	}
	var methods []easyPayCustomMethod
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return nil
	}
	result := make([]easyPayCustomMethod, 0, len(methods))
	for _, method := range methods {
		method.Type = strings.TrimSpace(method.Type)
		method.UpstreamType = strings.TrimSpace(method.UpstreamType)
		method.DisplayName = strings.TrimSpace(method.DisplayName)
		if method.Type == "" || method.UpstreamType == "" {
			continue
		}
		result = append(result, method)
	}
	return result
}

func (e *EasyPay) upstreamPaymentType(paymentType string) string {
	paymentType = strings.TrimSpace(paymentType)
	for _, method := range e.customMethods() {
		if paymentType == method.Type {
			return method.UpstreamType
		}
	}
	return paymentType
}

func (e *EasyPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if strings.EqualFold(strings.TrimSpace(e.config["queryMode"]), easypayQueryModeFindOrder) {
		return e.queryOrderFindOrder(ctx, tradeNo)
	}

	resp, err := e.queryOrderLegacy(ctx, tradeNo)
	if err == nil {
		return resp, nil
	}

	findResp, findErr := e.queryOrderFindOrder(ctx, tradeNo)
	if findErr == nil {
		return findResp, nil
	}
	return nil, fmt.Errorf("easypay query legacy: %v; findorder: %w", err, findErr)
}

type easyPayOrderQueryPayload struct {
	Code        int             `json:"code"`
	Msg         string          `json:"msg"`
	PID         string          `json:"pid"`
	Status      int             `json:"status"`
	TradeStatus string          `json:"trade_status"`
	Money       string          `json:"money"`
	TradeNo     string          `json:"trade_no"`
	OutTradeNo  string          `json:"out_trade_no"`
	OrderNo     string          `json:"order_no"`
	Data        json.RawMessage `json:"data"`
}

func (e *EasyPay) queryOrderLegacy(ctx context.Context, outTradeNo string) (*payment.QueryOrderResponse, error) {
	params := map[string]string{
		"act":          "order",
		"pid":          e.config["pid"],
		"key":          e.config["pkey"],
		"out_trade_no": outTradeNo,
	}
	body, err := e.post(ctx, e.apiBase()+"/api.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay query: %w", err)
	}
	return e.parseOrderQueryResponse(body, outTradeNo)
}

func (e *EasyPay) queryOrderFindOrder(ctx context.Context, outTradeNo string) (*payment.QueryOrderResponse, error) {
	params := map[string]string{
		"order_no": outTradeNo,
		"type":     easypayFindOrderNoType,
	}
	body, err := e.post(ctx, e.apiBase()+"/api/findorder", params)
	if err != nil {
		return nil, fmt.Errorf("easypay findorder query: %w", err)
	}
	return e.parseOrderQueryResponse(body, outTradeNo)
}

func (e *EasyPay) parseOrderQueryResponse(body []byte, queryRef string) (*payment.QueryOrderResponse, error) {
	payload, err := parseEasyPayOrderQueryPayload(body, queryRef)
	if err != nil {
		return nil, err
	}
	if payload.Code != 0 && !easyPaySuccessCode(payload.Code) {
		return nil, fmt.Errorf("easypay query error: %s", payload.Msg)
	}

	status := payment.ProviderStatusPending
	if payload.Status == easypayStatusPaid || strings.EqualFold(strings.TrimSpace(payload.TradeStatus), tradeStatusSuccess) {
		status = payment.ProviderStatusPaid
	}
	amount, _ := strconv.ParseFloat(payload.Money, 64)
	tradeNo := strings.TrimSpace(payload.TradeNo)
	if tradeNo == "" {
		tradeNo = strings.TrimSpace(queryRef)
	}
	metadata := e.MerchantIdentityMetadata()
	if pid := strings.TrimSpace(payload.PID); pid != "" {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["pid"] = pid
	}
	return &payment.QueryOrderResponse{
		TradeNo:  tradeNo,
		Status:   status,
		Amount:   amount,
		Metadata: metadata,
	}, nil
}

func parseEasyPayOrderQueryPayload(body []byte, queryRef string) (*easyPayOrderQueryPayload, error) {
	var root easyPayOrderQueryPayload
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("easypay parse query: %w", err)
	}
	if data := strings.TrimSpace(string(root.Data)); data != "" && data != "null" {
		if nested, ok := parseEasyPayNestedOrderPayload(root.Data, queryRef); ok {
			mergeEasyPayOrderQueryPayload(&root, nested)
		}
	}
	return &root, nil
}

func parseEasyPayNestedOrderPayload(data []byte, queryRef string) (*easyPayOrderQueryPayload, bool) {
	var nested easyPayOrderQueryPayload
	if err := json.Unmarshal(data, &nested); err == nil {
		return &nested, true
	}

	var rows []easyPayOrderQueryPayload
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 {
		return nil, false
	}
	queryRef = strings.TrimSpace(queryRef)
	for i := range rows {
		if strings.EqualFold(strings.TrimSpace(rows[i].OutTradeNo), queryRef) || strings.EqualFold(strings.TrimSpace(rows[i].OrderNo), queryRef) || strings.EqualFold(strings.TrimSpace(rows[i].TradeNo), queryRef) {
			return &rows[i], true
		}
	}
	return &rows[0], true
}

func mergeEasyPayOrderQueryPayload(dst, src *easyPayOrderQueryPayload) {
	if dst == nil || src == nil {
		return
	}
	if src.Code != 0 {
		dst.Code = src.Code
	}
	if src.Msg != "" {
		dst.Msg = src.Msg
	}
	if src.PID != "" {
		dst.PID = src.PID
	}
	if src.Status != 0 {
		dst.Status = src.Status
	}
	if src.TradeStatus != "" {
		dst.TradeStatus = src.TradeStatus
	}
	if src.Money != "" {
		dst.Money = src.Money
	}
	if src.TradeNo != "" {
		dst.TradeNo = src.TradeNo
	}
	if src.OutTradeNo != "" {
		dst.OutTradeNo = src.OutTradeNo
	}
	if src.OrderNo != "" {
		dst.OrderNo = src.OrderNo
	}
}

func (e *EasyPay) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, fmt.Errorf("parse notify: %w", err)
	}
	// url.ParseQuery already decodes values — no additional decode needed.
	params := make(map[string]string)
	for k := range values {
		params[k] = values.Get(k)
	}
	sign := params["sign"]
	if sign == "" {
		return nil, fmt.Errorf("missing sign")
	}
	if !easyPayVerifySign(params, e.config["pkey"], sign) {
		return nil, fmt.Errorf("invalid signature")
	}
	status := payment.ProviderStatusFailed
	if params["trade_status"] == tradeStatusSuccess {
		status = payment.ProviderStatusSuccess
	}
	amount, _ := strconv.ParseFloat(params["money"], 64)

	metadata := e.MerchantIdentityMetadata()
	if pid := strings.TrimSpace(params["pid"]); pid != "" {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["pid"] = pid
	}
	return &payment.PaymentNotification{
		TradeNo: params["trade_no"], OrderID: params["out_trade_no"],
		Amount: amount, Status: status, RawData: rawBody, Metadata: metadata,
	}, nil
}

func (e *EasyPay) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	attempts := e.refundAttempts(req)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("easypay refund missing order identifier")
	}
	var firstErr error
	for i, attempt := range attempts {
		body, status, err := e.postRaw(ctx, e.apiBase()+"/api.php?act=refund", attempt.params)
		if err != nil {
			return nil, fmt.Errorf("easypay refund request: %w", err)
		}
		if err := parseEasyPayRefundResponse(status, body); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if i+1 < len(attempts) && isEasyPayRefundOrderNotFound(err) {
				continue
			}
			return nil, err
		}
		return &payment.RefundResponse{RefundID: attempt.refundID, Status: payment.ProviderStatusSuccess}, nil
	}
	return nil, firstErr
}

type easyPayRefundAttempt struct {
	params   map[string]string
	refundID string
}

func (e *EasyPay) refundAttempts(req payment.RefundRequest) []easyPayRefundAttempt {
	base := map[string]string{
		"pid": e.config["pid"], "key": e.config["pkey"], "money": req.Amount,
	}
	var attempts []easyPayRefundAttempt
	if orderID := strings.TrimSpace(req.OrderID); orderID != "" {
		params := cloneStringMap(base)
		params["out_trade_no"] = orderID
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: orderID})
	}
	if tradeNo := strings.TrimSpace(req.TradeNo); tradeNo != "" {
		params := cloneStringMap(base)
		params["trade_no"] = tradeNo
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: tradeNo})
	}
	return attempts
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isEasyPayRefundOrderNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "订单编号不存在") ||
		strings.Contains(msg, "订单不存在") ||
		strings.Contains(lower, "order not found") ||
		strings.Contains(lower, "not exist")
}

func parseEasyPayRefundResponse(status int, body []byte) error {
	summary := summarizeEasyPayResponse(body)
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("easypay refund HTTP %d: %s", status, summary)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("easypay refund empty response (HTTP %d): %s", status, summary)
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		(strings.HasPrefix(lower, "<") && strings.Contains(lower, "html")) {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}

	var resp struct {
		Code any    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}
	if !easyPaySuccessCode(resp.Code) {
		msg := strings.TrimSpace(resp.Msg)
		if msg == "" {
			msg = summary
		}
		return fmt.Errorf("easypay refund failed (HTTP %d): %s", status, msg)
	}
	return nil
}

func easyPaySuccessCode(code any) bool {
	switch v := code.(type) {
	case int:
		return v == easypayCodeSuccess || v == easypayCodeSuccessLegacy
	case int32:
		return int(v) == easypayCodeSuccess || int(v) == easypayCodeSuccessLegacy
	case int64:
		return int(v) == easypayCodeSuccess || int(v) == easypayCodeSuccessLegacy
	case float32:
		return int(v) == easypayCodeSuccess || int(v) == easypayCodeSuccessLegacy
	case float64:
		return int(v) == easypayCodeSuccess || int(v) == easypayCodeSuccessLegacy
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && (n == easypayCodeSuccess || n == easypayCodeSuccessLegacy)
	default:
		return false
	}
}

func summarizeEasyPayResponse(body []byte) string {
	summary := strings.Join(strings.Fields(string(body)), " ")
	if summary == "" {
		return "<empty>"
	}
	if len(summary) > maxEasypayErrorSummary {
		truncated := summary[:maxEasypayErrorSummary]
		for len(truncated) > 0 && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		return truncated + "..."
	}
	return summary
}

func (e *EasyPay) resolveCID(paymentType string) string {
	if strings.HasPrefix(paymentType, "alipay") {
		if v := e.config["cidAlipay"]; v != "" {
			return v
		}
		return e.config["cid"]
	}
	if v := e.config["cidWxpay"]; v != "" {
		return v
	}
	return e.config["cid"]
}

func (e *EasyPay) resolveSiteName() string {
	if v := strings.TrimSpace(e.config["siteName"]); v != "" {
		return v
	}
	return strings.TrimSpace(e.config["sitename"])
}

func (e *EasyPay) post(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	body, _, err := e.postRaw(ctx, endpoint, params)
	return body, err
}

func (e *EasyPay) postRaw(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: easypayHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func easyPaySign(params map[string]string, pkey string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	for i, k := range keys {
		if i > 0 {
			_ = buf.WriteByte('&')
		}
		_, _ = buf.WriteString(k + "=" + params[k])
	}
	_, _ = buf.WriteString(pkey)
	hash := md5.Sum([]byte(buf.String()))
	return hex.EncodeToString(hash[:])
}

func easyPayVerifySign(params map[string]string, pkey string, sign string) bool {
	return hmac.Equal([]byte(easyPaySign(params, pkey)), []byte(sign))
}
