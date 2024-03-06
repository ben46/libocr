package managed

import (
	"context"
	"fmt"
	"time"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/config"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/protocol"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/serialization/protobuf"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/shim"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"github.com/smartcontractkit/libocr/subprocesses"
)

// RunManagedOracle 运行一个“管理型”版本的 protocol.RunOracle。它处理配置更新和从 commontypes.BinaryNetworkEndpoint
// 转换为 protocol.NetworkEndpoint。
func RunManagedOracle(
	ctx context.Context,

	v2bootstrappers []commontypes.BootstrapperLocator,
	configOverrider types.ConfigOverrider,
	configTracker types.ContractConfigTracker,
	contractTransmitter types.ContractTransmitter,
	database types.Database,
	datasource types.DataSource,
	localConfig types.LocalConfig,
	logger loghelper.LoggerWithContext,
	monitoringEndpoint commontypes.MonitoringEndpoint,
	netEndpointFactory types.BinaryNetworkEndpointFactory,
	privateKeys types.PrivateKeys,
) {
	mo := managedOracleState{
		ctx: ctx,

		v2bootstrappers:     v2bootstrappers,
		configOverrider:     configOverrider,
		configTracker:       configTracker,
		contractTransmitter: contractTransmitter,
		database:            database,
		datasource:          datasource,
		localConfig:         localConfig,
		logger:              logger,
		monitoringEndpoint:  monitoringEndpoint,
		netEndpointFactory:  netEndpointFactory,
		privateKeys:         privateKeys,
	}
	mo.run()
}

type managedOracleState struct {
	ctx context.Context

	v2bootstrappers     []commontypes.BootstrapperLocator
	config              config.SharedConfig
	configOverrider     types.ConfigOverrider
	configTracker       types.ContractConfigTracker
	contractTransmitter types.ContractTransmitter
	database            types.Database
	datasource          types.DataSource
	localConfig         types.LocalConfig
	logger              loghelper.LoggerWithContext
	monitoringEndpoint  commontypes.MonitoringEndpoint
	netEndpointFactory  types.BinaryNetworkEndpointFactory
	privateKeys         types.PrivateKeys

	chTelemetry        chan<- *protobuf.TelemetryWrapper
	netEndpoint        *shim.SerializingEndpoint
	oracleCancel       context.CancelFunc
	oracleSubprocesses subprocesses.Subprocesses
	otherSubprocesses  subprocesses.Subprocesses
}

func (mo *managedOracleState) run() {
	{
		chTelemetry := make(chan *protobuf.TelemetryWrapper, 100)
		mo.chTelemetry = chTelemetry
		mo.otherSubprocesses.Go(func() {
			forwardTelemetry(mo.ctx, mo.logger, mo.monitoringEndpoint, chTelemetry)
		})
	}

	mo.otherSubprocesses.Go(func() {
		collectGarbage(mo.ctx, mo.database, mo.localConfig, mo.logger)
	})

	// 从数据库恢复配置，这样即使以太坊节点不工作，我们也可以运行。
	{
		var cc *types.ContractConfig
		ok := mo.otherSubprocesses.BlockForAtMost(
			mo.ctx,
			mo.localConfig.DatabaseTimeout,
			func(ctx context.Context) {
				cc = loadConfigFromDatabase(ctx, mo.database, mo.logger)
			},
		)
		if !ok {
			mo.logger.Error("ManagedOracle: 试图恢复配置时数据库超时", commontypes.LogFields{
				"timeout": mo.localConfig.DatabaseTimeout,
			})
		} else if cc != nil {
			mo.configChanged(*cc)
		}
	}

	// 只有在我们尝试从数据库加载配置之后才开始跟踪配置
	chNewConfig := make(chan types.ContractConfig, 5)
	mo.otherSubprocesses.Go(func() {
		TrackConfig(mo.ctx, mo.configTracker, mo.config.ConfigDigest, mo.localConfig, mo.logger, chNewConfig)
	})

	for {
		select {
		case change := <-chNewConfig:
			mo.logger.Info("ManagedOracle: 切换配置", commontypes.LogFields{
				"oldConfigDigest": mo.config.ConfigDigest.Hex(),
				"newConfigDigest": change.ConfigDigest.Hex(),
			})
			mo.configChanged(change)
		case <-mo.ctx.Done():
			mo.logger.Info("ManagedOracle: 渐进关闭", nil)
			mo.closeOracle()
			mo.otherSubprocesses.Wait()
			mo.logger.Info("ManagedOracle: 退出", nil)
			return // 完全退出 ManagedOracle 事件循环
		}
	}
}

func (mo *managedOracleState) closeOracle() {
	if mo.oracleCancel != nil {
		mo.oracleCancel()
		mo.oracleSubprocesses.Wait()
		err := mo.netEndpoint.Close()
		if err != nil {
			mo.logger.Error("ManagedOracle: 关闭 BinaryNetworkEndpoint 时出错", commontypes.LogFields{
				"error": err,
			})
			// 无法处理，尝试继续。
		}
		mo.oracleCancel = nil
		mo.netEndpoint = nil
	}
}

func (mo *managedOracleState) configChanged(contractConfig types.ContractConfig) {
	// 停止先前配置的任何操作
	mo.closeOracle()

	// 解码 contractConfig
	skipChainSpecificChecks := mo.localConfig.DevelopmentMode == types.EnableDangerousDevelopmentMode
	var err error
	var oid commontypes.OracleID
	mo.config, oid, err = config.SharedConfigFromContractConfig(
		mo.contractTransmitter.ChainID(),
		skipChainSpecificChecks,
		contractConfig,
		mo.privateKeys,
		mo.netEndpointFactory.PeerID(),
		mo.contractTransmitter.FromAddress(),
	)
	if err != nil {
		mo.logger.Error("ManagedOracle: 更新配置时出错", commontypes.LogFields{
			"error": err,
		})
		return
	}

	// 使用新配置运行
	peerIDs := []string{}
	for _, identity := range mo.config.OracleIdentities {
		peerIDs = append(peerIDs, identity.PeerID)
	}

	childLogger := mo.logger.MakeChild(commontypes.LogFields{
		"configDigest": fmt.Sprintf("%x", mo.config.ConfigDigest),
		"oid":          oid,
	})

	binNetEndpoint, err := mo.netEndpointFactory.NewEndpoint(mo.config.ConfigDigest, peerIDs,
		mo.v2bootstrappers, mo.config.F, computeTokenBucketRefillRate(mo.config.PublicConfig),
		computeTokenBucketSize())
	if err != nil {
		mo.logger.Error("ManagedOracle: NewEndpoint 时出错", commontypes.LogFields{
			"error":           err,
			"configDigest":    mo.config.ConfigDigest,
			"peerIDs":         peerIDs,
			"v2bootstrappers": mo.v2bootstrappers,
		})
		return
	}

	netEndpoint := shim.NewSerializingEndpoint(
		mo.chTelemetry,
		mo.config.ConfigDigest,
		binNetEndpoint,
		childLogger,
	)

	if err := netEndpoint.Start(); err != nil {
		mo.logger.Error("ManagedOracle: netEndpoint.Start() 时出错", commontypes.LogFields{
			"error":        err,
			"configDigest": mo.config.ConfigDigest,
		})
		return
	}

	mo.netEndpoint = netEndpoint
	oracleCtx, oracleCancel := context.WithCancel(mo.ctx)
	mo.oracleCancel = oracleCancel
	mo.oracleSubprocesses.Go(func() {
		defer oracleCancel()
		protocol.RunOracle(
			oracleCtx,
			mo.config,
			ConfigOverriderWrapper{mo.configOverrider},
			mo.contractTransmitter,
			mo.database,
			mo.datasource,
			oid,
			mo.privateKeys,
			mo.localConfig,
			childLogger,
			mo.netEndpoint,
			shim.MakeTelemetrySender(mo.chTelemetry, childLogger),
		)
	})

	childCtx, childCancel := context.WithTimeout(mo.ctx, mo.localConfig.DatabaseTimeout)
	defer childCancel()
	if err := mo.database.WriteConfig(childCtx, contractConfig); err != nil {
		mo.logger.ErrorIfNotCanceled("ManagedOracle: 将新配置写入数据库时出错", childCtx, commontypes.LogFields{
			"configDigest": mo.config.ConfigDigest,
			"config":       contractConfig,
			"error":        err,
		})
	}
}

func computeTokenBucketRefillRate(cfg config.PublicConfig) float64 {
	return (1.0*float64(time.Second)/float64(cfg.DeltaResend) +
		1.0*float64(time.Second)/float64(cfg.DeltaProgress) +
		1.0*float64(time.Second)/float64(cfg.DeltaRound) +
		3.0*float64(time.Second)/float64(cfg.DeltaRound) +
		2.0*float64(time.Second)/float64(cfg.DeltaRound)) * 2.0
}

func computeTokenBucketSize() int {
	return (2 + 6) * 2
}
