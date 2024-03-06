package config

import (
	"crypto/aes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"golang.org/x/crypto/curve25519"
)

const SharedSecretSize = 16 // 一个 128 位对称密钥
type encryptedSharedSecret [SharedSecretSize]byte

// SharedSecretEncryptions 是使用每个 Oracle 的 SharedSecretEncryptionPublicKey 加密的 SharedConfig.SharedSecret。
//
// 我们使用自定义加密方案以更节省空间（与标准 AEAD 方案、nacl crypto_box 等相比），
// 这样在传输到 OffchainAggregator 时可以节省 gas。
type SharedSecretEncryptions struct {
	// （由 dealer 选择的秘密密钥）* g，X25519 点
	DiffieHellmanPoint [curve25519.PointSize]byte

	// 明文 sharedSecret 的 keccak256。
	//
	// 由于 SharedSecretEncryptions 通过智能合约共享，每个 Oracle 将看到相同的 SharedSecretHash。
	// 在解密后，Oracles 可以将其 sharedSecret 与 SharedSecretHash 进行比较，以防止 dealer 拉锯式行为。
	SharedSecretHash common.Hash

	// 通过每个 Oracle 的密钥加密的共享密钥的加密。第 i 个 Oracle 可以如下恢复密钥：
	//
	// 1. key := Keccak256(DH(DiffieHellmanPoint, 进程的秘密密钥))[:16]
	// 2. sharedSecret := AES128DecryptBlock(key, Encryptions[i])
	//
	// 详见 Decrypt。
	Encryptions []encryptedSharedSecret
}

// Equal 检查两个 SharedSecretEncryptions 是否相等。
func (e SharedSecretEncryptions) Equal(e2 SharedSecretEncryptions) bool {
	if len(e.Encryptions) != len(e2.Encryptions) {
		return false
	}
	encsEqual := true
	for i := range e.Encryptions {
		encsEqual = encsEqual && e.Encryptions[i] == e2.Encryptions[i]
	}
	return encsEqual &&
		e.DiffieHellmanPoint == e2.DiffieHellmanPoint &&
		e.SharedSecretHash == e2.SharedSecretHash
}

// 使用 AES-128 解密一个块。
func aesDecryptBlock(key, ciphertext []byte) [16]byte {
	if len(key) != 16 {
		// 断言
		panic("密钥长度错误")
	}
	if len(ciphertext) != 16 {
		// 断言
		panic("密文长度错误")
	}

	cipher, err := aes.NewCipher(key)
	if err != nil {
		// 断言
		panic(fmt.Sprintf("在 aes.NewCipher 过程中出现意外错误: %v", err))
	}

	var plaintext [16]byte
	cipher.Decrypt(plaintext[:], ciphertext)
	return plaintext
}

// Decrypt 返回 sharedSecret。
func (e SharedSecretEncryptions) Decrypt(oid commontypes.OracleID, k types.PrivateKeys) (*[SharedSecretSize]byte, error) {
	if len(e.Encryptions) <= int(oid) {
		return nil, errors.New("oid 超出 SharedSecretEncryptions.Encryptions 范围")
	}

	dhPoint, err := k.ConfigDiffieHellman(&e.DiffieHellmanPoint)
	if err != nil {
		return nil, err
	}

	key := crypto.Keccak256(dhPoint[:])[:16]

	sharedSecret := aesDecryptBlock(key, e.Encryptions[int(oid)][:])

	if common.BytesToHash(crypto.Keccak256(sharedSecret[:])) != e.SharedSecretHash {
		return nil, errors.Errorf("解密后的 sharedSecret 哈希值不正确")
	}

	return &sharedSecret, nil
}
