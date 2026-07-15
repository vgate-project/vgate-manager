package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/pkg/crypto"
)

type AdminUtilHandler struct{}

func NewAdminUtilHandler() *AdminUtilHandler {
	return &AdminUtilHandler{}
}

func (h *AdminUtilHandler) GenerateX25519(c *gin.Context) {
	priv, pub, err := crypto.GenerateX25519KeyPair()
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.RealityKeyResponse{
		PrivateKey: priv,
		PublicKey:  pub,
	})
}
