package admin

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) accountCleanupService() (service.AccountCleanupService, error) {
	if h == nil || h.adminService == nil {
		return nil, infraerrors.InternalServer("ACCOUNT_CLEANUP_SERVICE_UNAVAILABLE", "account cleanup service is unavailable")
	}
	cleanupService, ok := h.adminService.(service.AccountCleanupService)
	if !ok {
		return nil, infraerrors.InternalServer("ACCOUNT_CLEANUP_SERVICE_UNAVAILABLE", "account cleanup service is unavailable")
	}
	return cleanupService, nil
}

// PreviewCleanup previews accounts matched by cleanup filters.
// POST /api/v1/admin/accounts/cleanup/preview
func (h *AccountHandler) PreviewCleanup(c *gin.Context) {
	var req service.AccountCleanupInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cleanupService, err := h.accountCleanupService()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result, err := cleanupService.PreviewAccountCleanup(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ExecuteCleanup deletes or moves accounts matched by cleanup filters.
// POST /api/v1/admin/accounts/cleanup
func (h *AccountHandler) ExecuteCleanup(c *gin.Context) {
	var req service.AccountCleanupInput
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	cleanupService, err := h.accountCleanupService()
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	result, err := cleanupService.ExecuteAccountCleanup(c.Request.Context(), &req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
