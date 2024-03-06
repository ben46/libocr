package config

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/smartcontractkit/libocr/gethwrappers/exposedoffchainaggregator"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
)

// makeConfigDigestArgs 函数返回 ABI 方法的输入参数
func makeConfigDigestArgs() abi.Arguments {
	// 从 JSON 格式的字符串中解析出 ABI
	abi, err := abi.JSON(strings.NewReader(
		exposedoffchainaggregator.ExposedOffchainAggregatorABI))
	if err != nil {
		// 断言
		panic(fmt.Sprintf("无法解析聚合器 ABI: %s", err.Error()))
	}
	return abi.Methods["exposedConfigDigestFromConfigData"].Inputs
}

var configDigestArgs = makeConfigDigestArgs()

// ConfigDigest 函数生成配置摘要
func ConfigDigest(
	contractAddress common.Address,
	configCount uint64,
	oracles []common.Address,
	transmitters []common.Address,
	threshold uint8,
	encodedConfigVersion uint64,
	config []byte,
) types.ConfigDigest {
	// 将参数打包成消息
	msg, err := configDigestArgs.Pack(
		contractAddress,
		configCount,
		oracles,
		transmitters,
		threshold,
		encodedConfigVersion,
		config,
	)
	if err != nil {
		// 断言
		panic(err)
	}
	// 计算消息的 Keccak256 哈希值
	rawHash := crypto.Keccak256(msg)
	configDigest := types.ConfigDigest{}
	if n := copy(configDigest[:], rawHash); n != len(configDigest) {
		// 断言
		panic("复制的数据太少")
	}
	return configDigest
}