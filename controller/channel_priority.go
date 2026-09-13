package controller

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
)

// A committed write must never invite mutation retry when only refresh failed.
func channelRouteResponse(c *gin.Context, result model.RouteCommitResult, err error, data any) {
	status := http.StatusOK
	body := gin.H{"success": err == nil, "committed": result.Committed, "degraded": result.RefreshError != nil, "refresh_error": "", "message": "", "data": data}
	if err != nil {
		status = http.StatusInternalServerError
		body["message"] = err.Error()
		if errors.Is(err, model.ErrChannelRevisionConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, model.ErrUndeclaredChannelRoute) {
			status = http.StatusBadRequest
		}
	}
	if err != nil && result.Committed {
		status = http.StatusOK
		body["partial"] = true
		body["message"] = "Database committed, but the operation did not complete. Do not resend the mutation; inspect the operation result."
	}
	if result.RefreshError != nil {
		body["refresh_error"] = "routing cache refresh failed"
		if err == nil {
			body["message"] = "Database committed; routing uses database fallback. Do not resend the mutation; repair the cache condition and retry refresh only."
		} else {
			body["message"] = "Database committed, but the operation and cache refresh did not complete. Do not resend the mutation; inspect the operation result."
		}
	}
	c.JSON(status, body)
}
func channelEditData(channel *model.Channel) any {
	revision := model.ChannelRevision(channel)
	copy := *channel
	copy.Key = ""
	clearChannelInfo(&copy)
	return struct {
		*model.Channel
		Revision string `json:"revision"`
	}{&copy, revision}
}
func GetChannelModelPriority(c *gin.Context) {
	group, modelKey := c.Query("group"), c.Query("model")
	if group == "" || modelKey == "" {
		c.JSON(400, gin.H{"success": false, "message": "group and model are required"})
		return
	}
	data, err := model.GetChannelModelPriorities(group, modelKey)
	if err != nil {
		channelRouteResponse(c, model.RouteCommitResult{}, err, nil)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": data})
}
func GetChannelModelPriorityOptions(c *gin.Context) {
	data, err := model.GetChannelModelPriorityOptions()
	if err != nil {
		channelRouteResponse(c, model.RouteCommitResult{}, err, nil)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": data})
}
func UpdateChannelModelPriority(c *gin.Context) {
	var req struct {
		Group     *string `json:"group"`
		Model     *string `json:"model"`
		ChannelID *int    `json:"channel_id"`
		Priority  *int64  `json:"priority"`
	}
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&req)
	if err == nil {
		var extra any
		if dec.Decode(&extra) != io.EOF {
			err = errors.New("one JSON object required")
		}
	}
	if err != nil || req.Group == nil || req.Model == nil || req.ChannelID == nil || req.Priority == nil || *req.Group == "" || *req.Model == "" || *req.ChannelID <= 0 {
		c.JSON(400, gin.H{"success": false, "committed": false, "degraded": false, "refresh_error": "", "message": "exact group, model, channel_id and integer priority are required"})
		return
	}
	result, err := model.SetChannelModelPriority(*req.Group, *req.Model, *req.ChannelID, *req.Priority)
	channelRouteResponse(c, result, err, nil)
}
