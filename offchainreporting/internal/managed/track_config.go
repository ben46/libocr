package managed

import (
	"context"
	"time"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/config"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"github.com/smartcontractkit/libocr/subprocesses"
)

type trackConfigState struct {
	ctx context.Context
	// 输入
	configTracker types.ContractConfigTracker
	localConfig   types.LocalConfig
	logger        loghelper.LoggerWithContext
	// 输出
	chChanges chan<- types.ContractConfig
	// 本地
	subprocesses subprocesses.Subprocesses
	configDigest types.ConfigDigest
}

func (state *trackConfigState) run() {
	// 启动后立即检查
	tCheckLatestConfigDetails := time.After(0)
	tResubscribe := time.After(0)

	var subscription types.ContractConfigSubscription
	var chSubscription <-chan types.ContractConfig

	for {
		select {
		case _, ok := <-chSubscription:
			if ok {
				// 立即检查新配置
				tCheckLatestConfigDetails = time.After(0 * time.Second)
				state.logger.Info("TrackConfig: 订阅已触发", nil)
			} else {
				chSubscription = nil
				subscription.Close()
				state.logger.Warn("TrackConfig: 订阅已关闭", nil)
				tResubscribe = time.After(0)
			}
		case <-tCheckLatestConfigDetails:
			change, awaitingConfirmation := state.checkLatestConfigDetails()
			state.logger.Debug("TrackConfig: 检查LatestConfigDetails", nil)

			// 如果正在等待确认，就更快地轮询
			if awaitingConfirmation {
				wait := 15 * time.Second
				if state.localConfig.ContractConfigTrackerPollInterval < wait {
					wait = state.localConfig.ContractConfigTrackerPollInterval
				}
				tCheckLatestConfigDetails = time.After(wait)
				state.logger.Info("TrackConfig: 等待确认新配置", commontypes.LogFields{
					"wait": wait,
				})
			} else {
				tCheckLatestConfigDetails = time.After(state.localConfig.ContractConfigTrackerPollInterval)
			}

			if change != nil {
				state.configDigest = change.ConfigDigest
				state.logger.Info("TrackConfig: 返回配置", commontypes.LogFields{
					"configDigest": change.ConfigDigest.Hex(),
				})
				select {
				case state.chChanges <- *change:
				case <-state.ctx.Done():
				}
			}
		case <-tResubscribe:
			subscribeCtx, subscribeCancel := context.WithTimeout(state.ctx, state.localConfig.BlockchainTimeout)
			var err error
			subscription, err = state.configTracker.SubscribeToNewConfigs(subscribeCtx)
			subscribeCancel()
			if err != nil {
				state.logger.ErrorIfNotCanceled(
					"TrackConfig: SubscribeToNewConfigs失败。稍后重试",
					subscribeCtx,
					commontypes.LogFields{
						"error":                                  err,
						"ContractConfigTrackerSubscribeInterval": state.localConfig.ContractConfigTrackerSubscribeInterval,
					},
				)
				tResubscribe = time.After(state.localConfig.ContractConfigTrackerSubscribeInterval)
			} else {
				chSubscription = subscription.Configs()
			}
		case <-state.ctx.Done():
		}

		// 确保及时退出
		select {
		case <-state.ctx.Done():
			state.logger.Debug("TrackConfig: 清理中", nil)
			if subscription != nil {
				subscription.Close()
			}
			state.subprocesses.Wait()
			state.logger.Debug("TrackConfig: 退出", nil)
			return
		default:
		}
	}
}

func (state *trackConfigState) checkLatestConfigDetails() (
	latestConfigDetails *types.ContractConfig, awaitingConfirmation bool,
) {
	bhCtx, bhCancel := context.WithTimeout(state.ctx, state.localConfig.BlockchainTimeout)
	defer bhCancel()
	blockheight, err := state.configTracker.LatestBlockHeight(bhCtx)
	if err != nil {
		state.logger.ErrorIfNotCanceled("TrackConfig: LatestBlockHeight()出错", bhCtx, commontypes.LogFields{
			"error": err,
		})
		return nil, false
	}

	detailsCtx, detailsCancel := context.WithTimeout(state.ctx, state.localConfig.BlockchainTimeout)
	defer detailsCancel()
	changedInBlock, latestConfigDigest, err := state.configTracker.LatestConfigDetails(detailsCtx)
	if err != nil {
		state.logger.ErrorIfNotCanceled("TrackConfig: LatestConfigDetails()出错", detailsCtx, commontypes.LogFields{
			"error": err,
		})
		return nil, false
	}
	if latestConfigDigest == (types.ConfigDigest{}) {
		state.logger.Warn("TrackConfig: LatestConfigDetails()返回了零configDigest。看起来合约尚未配置", commontypes.LogFields{
			"configDigest": latestConfigDigest,
		})
		return nil, false
	}
	if state.configDigest == latestConfigDigest {
		return nil, false
	}
	if !state.localConfig.SkipContractConfigConfirmations && blockheight < changedInBlock+uint64(state.localConfig.ContractConfigConfirmations)-1 {
		return nil, true
	}
	configCtx, configCancel := context.WithTimeout(state.ctx, state.localConfig.BlockchainTimeout)
	defer configCancel()
	contractConfig, err := state.configTracker.ConfigFromLogs(configCtx, changedInBlock)
	if err != nil {
		state.logger.ErrorIfNotCanceled("TrackConfig: LatestConfigDetails()出错", configCtx, commontypes.LogFields{
			"error": err,
		})
		return nil, true
	}
	if contractConfig.EncodedConfigVersion != config.EncodedConfigVersion {
		state.logger.Error("TrackConfig: 收到具有未知EncodedConfigVersion的配置更改",
			commontypes.LogFields{"versionReceived": contractConfig.EncodedConfigVersion})
		return nil, false
	}
	return &contractConfig, false
}

func TrackConfig(
	ctx context.Context,

	configTracker types.ContractConfigTracker,
	initialConfigDigest types.ConfigDigest,
	localConfig types.LocalConfig,
	logger loghelper.LoggerWithContext,

	chChanges chan<- types.ContractConfig,
) {
	state := trackConfigState{
		ctx,
		// 输入
		configTracker,
		localConfig,
		logger,
		// 输出
		chChanges,
		// 本地
		subprocesses.Subprocesses{},
		initialConfigDigest,
	}
	state.run()
}