package controller

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/minimax"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

func GetMiniMaxChannelUsage(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, fmt.Errorf("invalid channel id: %w", err))
		return
	}

	ch, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if ch == nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel not found"})
		return
	}
	if ch.Type != constant.ChannelTypeMiniMax {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "channel type is not MiniMax"})
		return
	}
	if ch.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "multi-key channel is not supported"})
		return
	}

	cfg, err := minimax.ParseRuntimeKeyConfig(ch.Key)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:            ch.Id,
			ChannelType:          ch.Type,
			ChannelBaseUrl:       ch.GetBaseURL(),
			ChannelSetting:       ch.GetSetting(),
			ChannelOtherSettings: ch.GetOtherSettings(),
		},
		OriginModelName: firstMiniMaxModel(ch),
	}

	snapshot, err := minimax.GetUsageSnapshot(info, cfg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    snapshot,
	})
}

func firstMiniMaxModel(ch *model.Channel) string {
	if ch == nil {
		return ""
	}
	models := ch.GetModels()
	if len(models) == 0 {
		return "MiniMax-M2.7"
	}
	return models[0]
}
