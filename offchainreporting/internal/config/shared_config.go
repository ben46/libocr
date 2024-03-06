package config

import (
	"bytes"
	cryptorand "crypto/rand"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"golang.org/x/crypto/sha3"
)

// SharedConfig 是所有运行协议实例的 Oracle 共享的配置。
// 它通过智能合约传播，但其中的部分是加密的，只有 Oracle 可以访问。
type SharedConfig struct {
	PublicConfig     // 继承自 PublicConfig 结构体
	SharedSecret *[SharedSecretSize]byte // 共享密钥
}

// LeaderSelectionKey 根据共享密钥生成领导者选择密钥。
func (c *SharedConfig) LeaderSelectionKey() [16]byte {
	var result [16]byte
	h := sha3.NewLegacyKeccak256()
	h.Write(c.SharedSecret[:])
	h.Write([]byte("chainlink offchain reporting v1 leader selection key"))

	copy(result[:], h.Sum(nil))
	return result
}

// TransmissionOrderKey 根据共享密钥生成传输顺序密钥。
func (c *SharedConfig) TransmissionOrderKey() [16]byte {
	var result [16]byte
	h := sha3.NewLegacyKeccak256()
	h.Write(c.SharedSecret[:])
	h.Write([]byte("chainlink offchain reporting v1 transmission order key"))

	copy(result[:], h.Sum(nil))
	return result
}

// SharedConfigFromContractConfig 根据合约配置生成共享配置。
func SharedConfigFromContractConfig(
	chainID *big.Int,
	skipChainSpecificChecks bool,
	change types.ContractConfig,
	privateKeys types.PrivateKeys,
	peerID string,
	transmitAddress common.Address,
) (SharedConfig, commontypes.OracleID, error) {
	publicConfig, encSharedSecret, err := publicConfigFromContractConfig(chainID, skipChainSpecificChecks, change)
	if err != nil {
		return SharedConfig{}, 0, err
	}

	// 在公共配置中查找 Oracle 的身份并验证其与私钥的匹配性
	oracleID := commontypes.OracleID(math.MaxUint8)
	{
		var found bool
		for i, identity := range publicConfig.OracleIdentities {
			address := privateKeys.PublicKeyAddressOnChain()
			offchainPublicKey := privateKeys.PublicKeyOffChain()
			if identity.OnChainSigningAddress == address {
				if !bytes.Equal(identity.OffchainPublicKey, offchainPublicKey) {
					return SharedConfig{}, 0, errors.Errorf(
						"OnChainSigningAddress (0x%x) in publicConfig matches "+
							"mine, but OffchainPublicKey does not: %v (config) vs %v (mine)",
						address, identity.OffchainPublicKey, offchainPublicKey)
				}
				if identity.PeerID != peerID {
					return SharedConfig{}, 0, errors.Errorf(
						"OnChainSigningAddress (0x%x) in publicConfig matches "+
							"mine, but PeerID does not: %v (config) vs %v (mine)",
						address, identity.PeerID, peerID)
				}
				if identity.TransmitAddress != transmitAddress {
					return SharedConfig{}, 0, errors.Errorf(
						"OnChainSigningAddress (0x%x) in publicConfig matches "+
							"mine, but TransmitAddress does not: 0x%x (config) vs 0x%x (mine)",
						address, identity.TransmitAddress, transmitAddress)
				}
				oracleID = commontypes.OracleID(i)
				found = true
			}
		}

		if !found {
			return SharedConfig{},
				0,
				errors.Errorf("Could not find my OnChainSigningAddress 0x%x in publicConfig", privateKeys.PublicKeyAddressOnChain())
		}
	}

	// 解密共享密钥
	x, err := encSharedSecret.Decrypt(oracleID, privateKeys)
	if err != nil {
		return SharedConfig{}, 0, errors.Wrapf(err, "could not decrypt shared secret")
	}

	return SharedConfig{
		publicConfig,
		x,
	}, oracleID, nil

}

// XXXContractSetConfigArgsFromSharedConfig 从共享配置设置合约配置参数。
func XXXContractSetConfigArgsFromSharedConfig(
	c SharedConfig,
	sharedSecretEncryptionPublicKeys []types.SharedSecretEncryptionPublicKey,
) (
	signers []common.Address,
	transmitters []common.Address,
	threshold uint8,
	encodedConfigVersion uint64,
	encodedConfig []byte,
	err error,
) {
	offChainPublicKeys := []types.OffchainPublicKey{}
	peerIDs := []string{}
	// 遍历共享配置中的 Oracle 身份，收集签名者、传输者、Offchain 公钥和 PeerID
	for _, identity := range c.OracleIdentities {
		signers = append(signers, common.Address(identity.OnChainSigningAddress))
		transmitters = append(transmitters, identity.TransmitAddress)
		offChainPublicKeys = append(offChainPublicKeys, identity.OffchainPublicKey)
		peerIDs = append(peerIDs, identity.PeerID)
	}
	threshold = uint8(c.F) // 设置阈值为共享配置中的 F 值
	encodedConfigVersion = 1 // 编码配置版本为 1
	// 编码配置参数并加密共享密钥
	encodedConfig = (setConfigEncodedComponents{
		c.DeltaProgress,
		c.DeltaResend,
		c.DeltaRound,
		c.DeltaGrace,
		c.DeltaC,
		c.AlphaPPB,
		c.DeltaStage,
		c.RMax,
		c.S,
		offChainPublicKeys,
		peerIDs,
		XXXEncryptSharedSecret(
			sharedSecretEncryptionPublicKeys,
			c.SharedSecret,
			cryptorand.Reader,
		),
	}).encode()
	err = nil
	return
}
