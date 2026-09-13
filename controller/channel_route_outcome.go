package controller

import "github.com/QuantumNous/new-api/model"

// ChannelRouteOutcome is the original transaction result, never current health.
type ChannelRouteOutcome struct {
	ChannelID    int    `json:"channel_id"`
	Committed    bool   `json:"committed"`
	Degraded     bool   `json:"degraded"`
	RefreshError string `json:"refresh_error"`
	Error        string `json:"error,omitempty"`
}

func channelRouteOutcome(id int, result model.RouteCommitResult, err error) ChannelRouteOutcome {
	out := ChannelRouteOutcome{ChannelID: id, Committed: result.Committed, Degraded: result.RefreshError != nil}
	if result.RefreshError != nil {
		out.RefreshError = "routing cache refresh failed"
	}
	if err != nil {
		out.Error = "channel operation failed"
	}
	return out
}
