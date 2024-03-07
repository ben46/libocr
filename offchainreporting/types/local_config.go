package types

import "time"

// EnableDangerousDevelopmentMode 常量表示启用危险的开发模式

// LocalConfig 包含了不由 OnchainAggregator.SetConfig 强制约定的特定于 oracle 的配置细节
type LocalConfig struct {
	// 区块链查询超时时间（通过 ContractConfigTracker 和 ContractTransmitter 进行中介）
	// （这是必要的，因为 Oracle 的操作是串行的，因此在链交互上永远阻塞将会破坏 Oracle）
	BlockchainTimeout time.Duration

	// 在执行链上配置更改之前等待的区块确认数量。这个值不需要很高（特别是不需要保护免受恶意重组的影响）。
	// 由于配置更改会产生一些额外开销，并且小型重组是相当常见的，建议的值在两到十之间。
	//
	// 恶意重组在这里并不比在区块链应用程序中一般更令人担忧：
	// 由于节点每隔 ContractConfigTrackerPollInterval.Seconds() 检查合约以获取最新配置，它们将在任何长于该间隔的间隔内对当前配置达成共识，
	// 只要最长链上的最新 setConfig 事务是稳定的。它们因此能够在轮询间隔之后继续报告，除非对手能够在每次轮询间隔期间重复重组该事务，这将导致能够审查任何事务。
	//
	// 请注意，1 个确认意味着事务/事件已在一个区块中被挖掘。
	// 0 个确认意味着事件将会在挖掘之前被识别，这目前不受支持。
	// 例如:
	// 当前区块高度：42
	// 改变区块高度：43
	// 合约配置确认数：1
	// 仍在等待
	//
	// 当前区块高度：43
	// 改变区块高度：43
	// 合约配置确认数：1
	// 已确认
	ContractConfigConfirmations uint16

	// SkipContractConfigConfirmations 允许完全禁用确认检查
	// 这在某些情况下可能很有用，例如具有即时最终性的 L2，本地区块编号与从 block.number 返回的链上值不匹配
	SkipContractConfigConfirmations bool

	// ContractConfigTracker 被查询以获取更新的链上配置的轮询间隔。建议的值在十五秒到两分钟之间。
	ContractConfigTrackerPollInterval time.Duration

	// 如果 ContractConfigTracker 订阅不存在，则尝试建立订阅的间隔。建议的值在两到五分钟之间。
	ContractConfigTrackerSubscribeInterval time.Duration

	// ContractTransmitter.Transmit 调用的超时时间。
	ContractTransmitterTransmitTimeout time.Duration

	// 数据库交互的超时时间。
	// （这是必要的，因为 Oracle 的操作是串行的，因此在观察上永远阻塞将会破坏 Oracle）
	DatabaseTimeout time.Duration

	// 使用 DataSource.Observe 方法进行观察时的超时时间。
	// （这是必要的，因为 Oracle 的操作是串行的，因此在观察上永远阻塞将会破坏 Oracle）
	DataSourceTimeout time.Duration

	// 在 DataSourceTimeout 过期后，我们还会等待这个优雅期，等待 DataSource.Observe 返回结果，然后强制继续。
	DataSourceGracePeriod time.Duration

	// DANGER，这会关闭所有类型的健全性检查。 可能对测试有用。
	// 将此设置为 EnableDangerousDevelopmentMode 以开启开发模式。
	DevelopmentMode string
}