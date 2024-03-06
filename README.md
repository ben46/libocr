# OffChain Reporting

链下报告（OCR）是增加 Chainlink 网络分散化和可扩展性的重要一步。请查阅 OCR 协议论文以深入了解技术细节。

对于链下报告聚合器，所有节点使用点对点网络进行通信。在通信过程中，运行轻量级共识算法，每个节点报告其数据观察并对其进行签名。然后传输单一的聚合交易，这样可以节省大量 gas。

聚合交易中包含的报告由 Oracle 法定人数签名，并包含所有 Oracle 的观察。通过在链上验证报告并在链上检查法定人数的签名，我们保留了 Chainlink Oracle 网络的无信任属性。

# libocr

libocr 包括一个 Go 库和一组 Solidity 智能合约，实现了*Chainlink Offchain Reporting Protocol*，这是一个[拜占庭容错](https://en.wikipedia.org/wiki/Byzantine_fault)协议，允许一组 Oracle 在链下生成一个聚合报告，汇总了这些 Oracle 对某个基础数据源的观察。然后将此报告以单个交易的形式传输到链上合约。

您可能也对[将 libocr 集成到实际 Chainlink 节点中](https://github.com/smartcontractkit/chainlink/tree/develop/core/services/offchainreporting)感兴趣。

## 协议描述

协议执行主要在 Chainlink 节点之间的链下点对点网络上进行。节点定期选举一个新的领导节点，领导节点驱动协议的其余部分。该协议设计为公平地选择每个领导者，并且快速地从未能朝着及时提交链上报告的领导者身上转移出来。

领导者定期请求跟随者提供新签名的观察结果，并将它们聚合成一个报告。然后将聚合报告发送回跟随者，并要求他们通过签名来证明报告的有效性。如果有足够多的跟随者批准了该报告，领导者将组装一个带有该法定人数签名的最终报告，并将其广播给所有跟随者。

然后，节点尝试根据随机的时间表将最终报告传输到智能合约。最后，智能合约验证是否有足够多的节点签署了该报告，并向消费者公开中值。

## 组织
```
.
├── contract：以太坊智能合约
├── gethwrappers：OCR1 合约的 go-ethereum 绑定，使用 abigen 生成
├── gethwrappers2：OCR2 合约的 go-ethereum 绑定，使用 abigen 生成
├── networking：点对点网络层
├── offchainreporting：链下报告协议版本 1
├── offchainreporting2：链下报告协议版本 2 的特定包，这里没有太多内容
├── offchainreporting2plus：链下报告协议版本 2 及更高版本
├── permutation：用于生成排列的辅助包
└── subprocesses：用于管理 go 协程的辅助包
```