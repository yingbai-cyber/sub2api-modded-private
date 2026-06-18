package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

func registerAccountCleanupRoutes(accounts *gin.RouterGroup, h *handler.Handlers) {
	accounts.POST("/cleanup/preview", h.Admin.Account.PreviewCleanup)
	accounts.POST("/cleanup", h.Admin.Account.ExecuteCleanup)
}
