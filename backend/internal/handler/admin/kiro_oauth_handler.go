package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// KiroOAuthHandler handles Kiro OAuth authorization endpoints.
type KiroOAuthHandler struct {
	kiroOAuthService *service.KiroOAuthService
}

// NewKiroOAuthHandler creates a new KiroOAuthHandler.
func NewKiroOAuthHandler(kiroOAuthService *service.KiroOAuthService) *KiroOAuthHandler {
	return &KiroOAuthHandler{kiroOAuthService: kiroOAuthService}
}

// --- Request types ---

// KiroGenerateAuthURLRequest is the request for social/external-idp auth URL generation.
type KiroGenerateAuthURLRequest struct {
	AuthMethod string `json:"auth_method"` // "social" or "external_idp"
	Region     string `json:"region"`
	ProxyID    *int64 `json:"proxy_id"`
	IssuerURL  string `json:"issuer_url"`
	ClientID   string `json:"client_id"`
	Scopes     string `json:"scopes"`
}

// KiroGenerateIDCAuthURLRequest is the request for IDC (Builder ID) auth URL generation.
type KiroGenerateIDCAuthURLRequest struct {
	Region   string `json:"region"`
	StartURL string `json:"start_url"`
	ProxyID  *int64 `json:"proxy_id"`
}

// KiroExchangeCodeRequest is the request for code exchange.
type KiroExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	State     string `json:"state" binding:"required"`
	Code      string `json:"code" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// KiroRefreshTokenRequest is the request for token refresh validation.
type KiroRefreshTokenRequest struct {
	RefreshToken  string `json:"refresh_token" binding:"required"`
	AuthMethod    string `json:"auth_method"`
	Region        string `json:"region"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	TokenEndpoint string `json:"token_endpoint"`
	Scopes        string `json:"scopes"`
	ProxyID       *int64 `json:"proxy_id"`
}

// KiroImportTokenRequest is the request for token import.
type KiroImportTokenRequest struct {
	TokenJSON string `json:"token_json" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

// --- Handlers ---

// GenerateAuthURL generates a Kiro social or external IdP authorization URL.
// POST /api/v1/admin/kiro/oauth/auth-url
func (h *KiroOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req KiroGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.kiroOAuthService.GenerateAuthURL(c.Request.Context(), &service.KiroOAuthGenerateAuthURLInput{
		AuthMethod: req.AuthMethod,
		Region:     req.Region,
		ProxyID:    req.ProxyID,
		IssuerURL:  req.IssuerURL,
		ClientID:   req.ClientID,
		Scopes:     req.Scopes,
	})
	if err != nil {
		response.InternalError(c, "生成授权链接失败: "+err.Error())
		return
	}

	response.Success(c, result)
}

// GenerateIDCAuthURL generates an AWS Builder ID / IAM Identity Center auth URL.
// POST /api/v1/admin/kiro/oauth/idc-auth-url
func (h *KiroOAuthHandler) GenerateIDCAuthURL(c *gin.Context) {
	var req KiroGenerateIDCAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.kiroOAuthService.GenerateIDCAuthURL(c.Request.Context(), &service.KiroOAuthGenerateIDCAuthURLInput{
		Region:   req.Region,
		StartURL: req.StartURL,
		ProxyID:  req.ProxyID,
	})
	if err != nil {
		response.InternalError(c, "生成 IDC 授权链接失败: "+err.Error())
		return
	}

	response.Success(c, result)
}

// ExchangeCode exchanges an authorization code for tokens.
// POST /api/v1/admin/kiro/oauth/exchange-code
func (h *KiroOAuthHandler) ExchangeCode(c *gin.Context) {
	var req KiroExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.kiroOAuthService.ExchangeCode(c.Request.Context(), &service.KiroOAuthExchangeCodeInput{
		SessionID: req.SessionID,
		State:     req.State,
		Code:      req.Code,
		ProxyID:   req.ProxyID,
	})
	if err != nil {
		response.BadRequest(c, "Token 交换失败: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshToken validates a refresh token by performing a refresh.
// POST /api/v1/admin/kiro/oauth/refresh-token
func (h *KiroOAuthHandler) RefreshToken(c *gin.Context) {
	var req KiroRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.kiroOAuthService.ValidateRefreshToken(c.Request.Context(), &service.KiroOAuthRefreshTokenInput{
		RefreshToken:  req.RefreshToken,
		AuthMethod:    req.AuthMethod,
		Region:        req.Region,
		ClientID:      req.ClientID,
		ClientSecret:  req.ClientSecret,
		TokenEndpoint: req.TokenEndpoint,
		Scopes:        req.Scopes,
		ProxyID:       req.ProxyID,
	})
	if err != nil {
		response.BadRequest(c, "Token 刷新失败: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}

// ImportToken imports a Kiro IDE token JSON and validates it.
// POST /api/v1/admin/kiro/oauth/import-token
func (h *KiroOAuthHandler) ImportToken(c *gin.Context) {
	var req KiroImportTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	tokenInfo, err := h.kiroOAuthService.ImportToken(c.Request.Context(), req.TokenJSON, req.ProxyID)
	if err != nil {
		response.BadRequest(c, "Token 导入失败: "+err.Error())
		return
	}

	response.Success(c, tokenInfo)
}
