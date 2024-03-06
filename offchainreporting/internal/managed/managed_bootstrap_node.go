package managed

import (
	"context"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/config"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"github.com/smartcontractkit/libocr/subprocesses"
)

// RunManagedBootstrapNode 运行一个"托管"的引导节点。它处理合同上的配置更新。
func RunManagedBootstrapNode(
	ctx context.Context,
	bootstrapperFactory types.BootstrapperFactory,
	v2bootstrappers []commontypes.BootstrapperLocator,
	contractConfigTracker types.ContractConfigTracker,
	database types.Database,
	localConfig types.LocalConfig,
	logger loghelper.LoggerWithContext,
) {
	mb := managedBootstrapNodeState{
		ctx:                 ctx,
		bootstrapperFactory: bootstrapperFactory,
		v2bootstrappers:     v2bootstrappers,
		configTracker:       contractConfigTracker,
		database:            database,
		localConfig:         localConfig,
		logger:              logger,
	}
	mb.run()
}

type managedBootstrapNodeState struct {
	ctx                 context.Context
	v2bootstrappers     []commontypes.BootstrapperLocator
	bootstrapperFactory types.BootstrapperFactory
	configTracker       types.ContractConfigTracker
	database            types.Database
	localConfig         types.LocalConfig
	logger              loghelper.LoggerWithContext
	bootstrapper        commontypes.Bootstrapper
	config              config.PublicConfig
}

func (mb *managedBootstrapNodeState) run() {
	var subprocesses subprocesses.Subprocesses

	// 从数据库恢复配置，以便即使以太坊节点不工作，我们也可以运行。
	{
		var cc *types.ContractConfig
		ok := subprocesses.BlockForAtMost(
			mb.ctx,
			mb.localConfig.DatabaseTimeout,
			func(ctx context.Context) {
				cc = loadConfigFromDatabase(ctx, mb.database, mb.logger)
			},
		)
		if !ok {
			mb.logger.Error("ManagedBootstrapNode: 尝试恢复配置时数据库超时", commontypes.LogFields{
				"timeout": mb.localConfig.DatabaseTimeout,
			})
		} else if cc != nil {
			mb.configChanged(*cc)
		}
	}

	chNewConfig := make(chan types.ContractConfig, 5)
	subprocesses.Go(func() {
		TrackConfig(mb.ctx, mb.configTracker, mb.config.ConfigDigest, mb.localConfig, mb.logger, chNewConfig)
	})

	for {
		select {
		case cc := <-chNewConfig:
			mb.logger.Info("ManagedBootstrapNode: 切换配置", commontypes.LogFields{
				"oldConfigDigest": mb.config.ConfigDigest.Hex(),
				"newConfigDigest": cc.ConfigDigest.Hex(),
			})
			mb.configChanged(cc)
		case <-mb.ctx.Done():
			mb.logger.Debug("ManagedBootstrapNode: 正在关闭", nil)
			mb.closeBootstrapper()
			subprocesses.Wait()
			mb.logger.Debug("ManagedBootstrapNode: 退出", nil)
			return
		}
	}
}

func (mb *managedBootstrapNodeState) closeBootstrapper() {
	if mb.bootstrapper != nil {
		err := mb.bootstrapper.Close()
		// 除了记录错误和祈祷外，我们无法做太多事情
		if err != nil {
			mb.logger.Error("ManagedBootstrapNode: 关闭引导程序时出错", commontypes.LogFields{
				"error": err,
			})
		}
		mb.bootstrapper = nil
	}
}

func (mb *managedBootstrapNodeState) configChanged(cc types.ContractConfig) {
	// 停止之前配置的任何操作
	mb.closeBootstrapper()

	var err error
	// 我们可以在此跳过特定于链的检查。引导节点不使用任何特定于链的参数，
	// 因为它不参与实际的 OCR 协议。它只是在 P2P 网络中挂出并帮助其他节点相互发现。
	mb.config, err = config.PublicConfigFromContractConfig(nil, true, cc)
	if err != nil {
		mb.logger.Error("ManagedBootstrapNode: 解码 ContractConfig 时出错", commontypes.LogFields{
			"error": err,
		})
		return
	}

	peerIDs := []string{}
	for _, pcKey := range mb.config.OracleIdentities {
		peerIDs = append(peerIDs, pcKey.PeerID)
	}

	bootstrapper, err := mb.bootstrapperFactory.NewBootstrapper(mb.config.ConfigDigest, peerIDs, mb.v2bootstrappers, mb.config.F)
	if err != nil {
		mb.logger.Error("ManagedBootstrapNode: NewBootstrapper 期间出错", commontypes.LogFields{
			"error":           err,
			"configDigest":    mb.config.ConfigDigest,
			"peerIDs":         peerIDs,
			"v2boot

strappers": mb.v2bootstrappers,
		})
		return
	}
	err = bootstrapper.Start()
	if err != nil {
		mb.logger.Error("ManagedBootstrapNode: bootstrapper.Start() 期间出错", commontypes.LogFields{
			"error":        err,
			"configDigest": mb.config.ConfigDigest,
		})
		return
	}

	mb.bootstrapper = bootstrapper

	childCtx, childCancel := context.WithTimeout(mb.ctx, mb.localConfig.DatabaseTimeout)
	defer childCancel()
	if err := mb.database.WriteConfig(childCtx, cc); err != nil {
		mb.logger.ErrorIfNotCanceled("ManagedBootstrapNode: 向数据库写入新配置时出错", childCtx, commontypes.LogFields{
			"config": cc,
			"error":  err,
		})
		// 即使不将配置存储在数据库中，我们也可以继续运行
	}
}