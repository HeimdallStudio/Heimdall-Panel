package controller

import (
	"net/http"
	"strings"

	coreiplimit "github.com/mhsanaei/3x-ui/v3/internal/iplimit"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

type StrictIPLimitAuthorityController struct {
	service service.StrictIPLimitService
}

func NewStrictIPLimitAuthorityController(g *gin.RouterGroup) *StrictIPLimitAuthorityController {
	c := &StrictIPLimitAuthorityController{}
	g.POST("/lease", c.lease)
	return c
}

func (a *StrictIPLimitAuthorityController) lease(c *gin.Context) {
	token := strings.TrimSpace(c.GetHeader(coreiplimit.AuthorityHeader))
	childGuid, err := a.service.AuthenticateDirectChild(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, coreiplimit.LeaseResponse{Allowed: false, Error: "unauthorized strict ip-limit relay"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req coreiplimit.LeaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, coreiplimit.LeaseResponse{Allowed: false, Error: "invalid strict ip-limit request"})
		return
	}

	resp, err := a.service.ResolveRelay(c.Request.Context(), req, childGuid)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, coreiplimit.LeaseResponse{Allowed: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}
