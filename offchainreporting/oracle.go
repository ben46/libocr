// Package offchainreporting 提供了运行离链报告协议的功能。
package offchainreporting

import (
	"context"
	"fmt"
	"sync"

	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/managed"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"github.com/smartcontractkit/libocr/subprocesses"
)

// OracleArgs 包含了调用者必须提供的配置和服务，以便运行离链报告协议。
//
// 除非另有说明，否则所有字段都应为非空。
type OracleArgs struct {
	// 用于生成网络端点的工厂。网络端点包含了消费者必须实现的网络方法，以允许节点与其他参与节点通信。
	BinaryNetworkEndpointFactory types.BinaryNetworkEndpointFactory

	// V2Bootstrappers 是 v2 栈的引导节点地址和 ID 列表
	V2Bootstrappers []commontypes.BootstrapperLocator

	// 允许本地覆盖某些配置参数。例如，适用于休眠模式。可能为 nil。
	ConfigOverrider types.ConfigOverrider

	// 与 OffchainAggregator 智能合约的传输相关逻辑进行接口交互
	ContractTransmitter types.ContractTransmitter

	// 跟踪配置更改
	ContractConfigTracker types.ContractConfigTracker

	// 数据库提供持久化存储
	Database types.Database

	// 用于观察节点应就共识达成的值
	Datasource types.DataSource

	// LocalConfig 包含了与 Oracle 相关的配置详情，这些详情不受通过 OffchainAggregator.SetConfig 指定的链上配置规范的强制约束
	LocalConfig types.LocalConfig

	// Logger 用于记录各种信息
	Logger commontypes.Logger

	// 用于将日志发送到监视器。可能为 nil。
	MonitoringEndpoint commontypes.MonitoringEndpoint

	// PrivateKeys 包含了 OCR 协议所需的秘密密钥，以及使用这些密钥的方法，而不会将其暴露给应用程序的其他部分。
	PrivateKeys types.PrivateKeys
}

type oracleState int

const (
	oracleStateUnstarted oracleState = iota
	oracleStateStarted
	oracleStateClosed
)

type Oracle struct {
	lock         sync.Mutex
	state        oracleState
	oracleArgs   OracleArgs
	subprocesses subprocesses.Subprocesses
	cancel       context.CancelFunc
}

// NewOracle 使用提供的服务和配置返回一个新初始化的 Oracle。
func NewOracle(args OracleArgs) (*Oracle, error) {
	if err := SanityCheckLocalConfig(args.LocalConfig); err != nil {
		return nil, errors.Wrapf(err, "创建新 Oracle 时出现错误的本地配置")
	}
	return &Oracle{
		lock:         sync.Mutex{},
		state:        oracleStateUnstarted,
		oracleArgs:   args,
		subprocesses: subprocesses.Subprocesses{},
		cancel:       nil,
	}, nil
}

// Start 启动 Oracle。
func (o *Oracle) Start() error {
	o.lock.Lock()
	defer o.lock.Unlock()

	if o.state != oracleStateUnstarted {
		return fmt.Errorf("只能启动一次 Oracle")
	}
	o.state = oracleStateStarted

	logger := loghelper.MakeRootLoggerWithContext(o.oracleArgs.Logger)

	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel

	o.subprocesses.Go(func() {
		defer cancel()
		managed.RunManagedOracle(
			ctx,
			o.oracleArgs.V2Bootstrappers,
			o.oracleArgs.ConfigOverrider,
			o.oracleArgs.ContractConfigTracker,
			o.oracleArgs.ContractTransmitter,
			o.oracleArgs.Database,
			o.oracleArgs.Datasource,
			o.oracleArgs.LocalConfig,
			logger,
			o.oracleArgs.MonitoringEndpoint,
			o.oracleArgs.BinaryNetworkEndpointFactory,
			o.oracleArgs.PrivateKeys,
		)
	})
	return nil
}

// Close 关闭 Oracle。可以安全地多次调用。
func (o *Oracle) Close() error {
	o.lock.Lock()
	defer o.lock.Unlock()

	if o.state != oracleStateStarted {
		return fmt.Errorf("只能关闭已启动的 Oracle")
	}
	o.state = oracleStateClosed

	if o.cancel != nil {
		o.cancel()
	}

	// 在关闭其他资源之前，等待所有子进程关闭。
	// （不希望发生尝试使用已关闭资源而导致恐慌。）
	o.subprocesses.Wait()
	return nil
}