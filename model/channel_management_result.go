package model

import "github.com/QuantumNous/new-api/common"

func (channel *Channel) DeleteWithResult() (RouteCommitResult, error) {
	_, result, err := deleteRouteChannelsWithResult("id = ?", channel.Id)
	return result, err
}
func BatchDeleteChannelsWithResult(ids []int) (int64, RouteCommitResult, error) {
	return deleteRouteChannelsWithResult("id IN ?", ids)
}
func DeleteDisabledChannelWithResult() (int64, RouteCommitResult, error) {
	return deleteRouteChannelsWithResult("status IN ?", []int{common.ChannelStatusAutoDisabled, common.ChannelStatusManuallyDisabled})
}
func EnableChannelByTagWithResult(tag string) (RouteCommitResult, error) {
	return setRouteTagStatusWithResult(tag, common.ChannelStatusEnabled)
}
func DisableChannelByTagWithResult(tag string) (RouteCommitResult, error) {
	return setRouteTagStatusWithResult(tag, common.ChannelStatusManuallyDisabled)
}
