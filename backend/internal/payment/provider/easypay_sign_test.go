package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPaySignConsistentOutput(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER123",
		"name":         "Test Product",
		"money":        "10.00",
	}
	pkey := "test_secret_key"

	sign1 := easyPaySign(params, pkey)
	sign2 := easyPaySign(params, pkey)
	if sign1 != sign2 {
		t.Fatalf("easyPaySign should be deterministic: %q != %q", sign1, sign2)
	}
	if len(sign1) != 32 {
		t.Fatalf("MD5 hex should be 32 chars, got %d", len(sign1))
	}
}

func TestEasyPaySignExcludesSignAndSignType(t *testing.T) {
	t.Parallel()

	pkey := "my_key"
	base := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	withSign := map[string]string{
		"pid":       "1001",
		"type":      "alipay",
		"sign":      "should_be_ignored",
		"sign_type": "MD5",
	}

	signBase := easyPaySign(base, pkey)
	signWithExtra := easyPaySign(withSign, pkey)

	if signBase != signWithExtra {
		t.Fatalf("sign and sign_type should be excluded: base=%q, withExtra=%q", signBase, signWithExtra)
	}
}

func TestEasyPaySignExcludesEmptyValues(t *testing.T) {
	t.Parallel()

	pkey := "key123"
	base := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	withEmpty := map[string]string{
		"pid":      "1001",
		"type":     "alipay",
		"device":   "",
		"clientip": "",
	}

	signBase := easyPaySign(base, pkey)
	signWithEmpty := easyPaySign(withEmpty, pkey)

	if signBase != signWithEmpty {
		t.Fatalf("empty values should be excluded: base=%q, withEmpty=%q", signBase, signWithEmpty)
	}
}

func TestEasyPayVerifySignValid(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER456",
		"money":        "25.00",
	}
	pkey := "secret"

	sign := easyPaySign(params, pkey)

	// Add sign to params (as would come in a real callback)
	params["sign"] = sign
	params["sign_type"] = "MD5"

	if !easyPayVerifySign(params, pkey, sign) {
		t.Fatal("easyPayVerifySign should return true for a valid signature")
	}
}

func TestEasyPayVerifySignTampered(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER789",
		"money":        "50.00",
	}
	pkey := "secret"

	sign := easyPaySign(params, pkey)

	// Tamper with the amount
	params["money"] = "99.99"

	if easyPayVerifySign(params, pkey, sign) {
		t.Fatal("easyPayVerifySign should return false for tampered params")
	}
}

func TestEasyPayVerifySignWrongKey(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":  "1001",
		"type": "wxpay",
	}

	sign := easyPaySign(params, "correct_key")

	if easyPayVerifySign(params, "wrong_key", sign) {
		t.Fatal("easyPayVerifySign should return false with wrong key")
	}
}

func TestEasyPaySignEmptyParams(t *testing.T) {
	t.Parallel()

	sign := easyPaySign(map[string]string{}, "key123")
	if sign == "" {
		t.Fatal("easyPaySign with empty params should still produce a hash")
	}
	if len(sign) != 32 {
		t.Fatalf("MD5 hex should be 32 chars, got %d", len(sign))
	}
}

func TestEasyPaySignSortOrder(t *testing.T) {
	t.Parallel()

	pkey := "test_key"
	params1 := map[string]string{
		"a": "1",
		"b": "2",
		"c": "3",
	}
	params2 := map[string]string{
		"c": "3",
		"a": "1",
		"b": "2",
	}

	sign1 := easyPaySign(params1, pkey)
	sign2 := easyPaySign(params2, pkey)

	if sign1 != sign2 {
		t.Fatalf("easyPaySign should be order-independent: %q != %q", sign1, sign2)
	}
}

func TestEasyPayVerifySignWrongSignValue(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	pkey := "key"

	if easyPayVerifySign(params, pkey, "00000000000000000000000000000000") {
		t.Fatal("easyPayVerifySign should return false for an incorrect sign value")
	}
}

func TestEasyPayMerchantIdentityMetadata(t *testing.T) {
	t.Parallel()

	provider := &EasyPay{
		config: map[string]string{
			"pid": "1001",
		},
	}

	metadata := provider.MerchantIdentityMetadata()
	if metadata["pid"] != "1001" {
		t.Fatalf("pid = %q, want %q", metadata["pid"], "1001")
	}
}

func TestEasyPayCreateAPIPaymentAcceptsEZFPYCodeAndQRCode(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm failed: %v", err)
		}
		gotForm = r.PostForm
		if gotPath != "/mapi.php" {
			t.Fatalf("path = %q, want /mapi.php", gotPath)
		}
		assertEasyPaySignedForm(t, gotForm, "pkey-1001")
		if gotForm.Get("sitename") != "Sub2API" {
			t.Fatalf("sitename = %q, want Sub2API", gotForm.Get("sitename"))
		}
		_, _ = io.WriteString(w, `{"code":200,"msg":"获取成功!","trade_no":"EP20260425001","qrcode":"qr-content"}`)
	}))
	defer server.Close()

	provider := newEasyPayTestProvider(t, server.URL, map[string]string{"siteName": "Sub2API"})
	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2_20260425ABC",
		Amount:      "70.00",
		PaymentType: payment.TypeAlipay,
		Subject:     "Plus 套餐",
		NotifyURL:   "https://merchant.example.com/notify",
		ReturnURL:   "https://merchant.example.com/return",
		ClientIP:    "203.0.113.10",
	})
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	if resp.TradeNo != "EP20260425001" {
		t.Fatalf("TradeNo = %q", resp.TradeNo)
	}
	if resp.QRCode != "qr-content" {
		t.Fatalf("QRCode = %q, want qr-content", resp.QRCode)
	}
	if gotForm.Get("out_trade_no") != "sub2_20260425ABC" {
		t.Fatalf("out_trade_no = %q", gotForm.Get("out_trade_no"))
	}
}

func TestEasyPayCreateAPIPaymentUsesCodeURLFallback(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mapi.php" {
			t.Fatalf("path = %q, want /mapi.php", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"code":200,"msg":"获取成功!","trade_no":"EP20260425002","code_url":"https://pay.example.com/qrcode.png"}`)
	}))
	defer server.Close()

	provider := newEasyPayTestProvider(t, server.URL, nil)
	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2_20260425DEF",
		Amount:      "130.00",
		PaymentType: payment.TypeWxpay,
		Subject:     "Pro 套餐",
	})
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	if resp.QRCode != "https://pay.example.com/qrcode.png" {
		t.Fatalf("QRCode = %q, want code_url fallback", resp.QRCode)
	}
}

func TestEasyPayQueryOrderFindOrderMode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/findorder" {
			t.Fatalf("path = %q, want /api/findorder", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm failed: %v", err)
		}
		if r.PostForm.Get("order_no") != "sub2_20260425XYZ" {
			t.Fatalf("order_no = %q", r.PostForm.Get("order_no"))
		}
		if r.PostForm.Get("type") != "1" {
			t.Fatalf("type = %q", r.PostForm.Get("type"))
		}
		_, _ = io.WriteString(w, `{"code":200,"msg":"获取成功!","data":{"pid":"2001","trade_no":"EPTRADE123","out_trade_no":"sub2_20260425XYZ","money":"188.00","trade_status":"TRADE_SUCCESS"}}`)
	}))
	defer server.Close()

	provider := newEasyPayTestProvider(t, server.URL, map[string]string{"queryMode": easypayQueryModeFindOrder})
	resp, err := provider.QueryOrder(context.Background(), "sub2_20260425XYZ")
	if err != nil {
		t.Fatalf("QueryOrder failed: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("Status = %q, want paid", resp.Status)
	}
	if resp.TradeNo != "EPTRADE123" {
		t.Fatalf("TradeNo = %q, want EPTRADE123", resp.TradeNo)
	}
	if resp.Amount != 188.00 {
		t.Fatalf("Amount = %.2f, want 188.00", resp.Amount)
	}
	if resp.Metadata["pid"] != "2001" {
		t.Fatalf("metadata pid = %q, want 2001", resp.Metadata["pid"])
	}
}

func TestEasyPayQueryOrderFallsBackToFindOrder(t *testing.T) {
	t.Parallel()

	var legacyCalled bool
	var findOrderCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.php":
			legacyCalled = true
			_, _ = io.WriteString(w, `{"code":400,"msg":"unsupported legacy query"}`)
		case "/api/findorder":
			findOrderCalled = true
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm failed: %v", err)
			}
			if r.PostForm.Get("order_no") != "sub2_20260425FALLBACK" {
				t.Fatalf("order_no = %q", r.PostForm.Get("order_no"))
			}
			_, _ = io.WriteString(w, `{"code":200,"msg":"获取成功!","data":[{"trade_no":"EPTRADEFALLBACK","out_trade_no":"sub2_20260425FALLBACK","money":"70.00","status":1}]}`)
		default:
			t.Fatalf("unexpected path: %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := newEasyPayTestProvider(t, server.URL, nil)
	resp, err := provider.QueryOrder(context.Background(), "sub2_20260425FALLBACK")
	if err != nil {
		t.Fatalf("QueryOrder failed: %v", err)
	}
	if !legacyCalled || !findOrderCalled {
		t.Fatalf("legacyCalled=%v findOrderCalled=%v, want both true", legacyCalled, findOrderCalled)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("Status = %q, want paid", resp.Status)
	}
	if resp.TradeNo != "EPTRADEFALLBACK" {
		t.Fatalf("TradeNo = %q, want EPTRADEFALLBACK", resp.TradeNo)
	}
}

func TestEasyPayVerifyNotificationSignedSuccess(t *testing.T) {
	t.Parallel()

	provider := newEasyPayTestProvider(t, "https://pay.example.com", nil)
	params := map[string]string{
		"pid":          "1001",
		"trade_no":     "EPTRADE_NOTIFY",
		"out_trade_no": "sub2_20260425NOTIFY",
		"type":         "alipay",
		"name":         "Plus 套餐",
		"money":        "70.00",
		"trade_status": tradeStatusSuccess,
	}
	params["sign"] = easyPaySign(params, "pkey-1001")
	params["sign_type"] = signTypeMD5

	notification, err := provider.VerifyNotification(context.Background(), encodeEasyPayParams(params), nil)
	if err != nil {
		t.Fatalf("VerifyNotification failed: %v", err)
	}
	if notification.Status != payment.ProviderStatusSuccess {
		t.Fatalf("Status = %q, want success", notification.Status)
	}
	if notification.TradeNo != "EPTRADE_NOTIFY" || notification.OrderID != "sub2_20260425NOTIFY" {
		t.Fatalf("notification trade/order = %q/%q", notification.TradeNo, notification.OrderID)
	}
	if notification.Amount != 70.00 {
		t.Fatalf("Amount = %.2f, want 70.00", notification.Amount)
	}
	if notification.Metadata["pid"] != "1001" {
		t.Fatalf("metadata pid = %q, want 1001", notification.Metadata["pid"])
	}
}

func newEasyPayTestProvider(t *testing.T, apiBase string, extra map[string]string) *EasyPay {
	t.Helper()
	config := map[string]string{
		"pid":       "1001",
		"pkey":      "pkey-1001",
		"apiBase":   strings.TrimRight(apiBase, "/"),
		"notifyUrl": "https://merchant.example.com/notify",
		"returnUrl": "https://merchant.example.com/return",
	}
	for key, value := range extra {
		config[key] = value
	}
	provider, err := NewEasyPay("test-instance", config)
	if err != nil {
		t.Fatalf("NewEasyPay failed: %v", err)
	}
	return provider
}

func assertEasyPaySignedForm(t *testing.T, form url.Values, pkey string) {
	t.Helper()
	params := make(map[string]string)
	for key := range form {
		params[key] = form.Get(key)
	}
	sign := params["sign"]
	if sign == "" {
		t.Fatal("missing sign")
	}
	if params["sign_type"] != signTypeMD5 {
		t.Fatalf("sign_type = %q, want MD5", params["sign_type"])
	}
	if !easyPayVerifySign(params, pkey, sign) {
		t.Fatalf("invalid signature for form: %v", form)
	}
}

func encodeEasyPayParams(params map[string]string) string {
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	return values.Encode()
}
