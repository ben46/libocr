package config

import (
	"crypto/aes"
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"golang.org/x/crypto/curve25519"
)

// XXXEncryptSharedSecretInternal 从一组 SharedSecretEncryptionPublicKeys、sharedSecret 和临时秘密密钥 sk 构造 SharedSecretEncryptions。
func XXXEncryptSharedSecretInternal(
	publicKeys []types.SharedSecretEncryptionPublicKey,
	sharedSecret *[SharedSecretSize]byte,
	sk *[32]byte,
) SharedSecretEncryptions {
	pk, err := curve25519.X25519(sk[:], curve25519.Basepoint)
	if err != nil {
		panic("在加密 sharedSecret 时: " + err.Error()) // XXX: 返回错误/记录日志
	}

	var pkArray [32]byte
	copy(pkArray[:], pk)

	encryptedSharedSecrets := []encryptedSharedSecret{}
	for _, pk := range publicKeys { // 使用每个 pk 加密 sharedSecret
		pkBytes := [32]byte(pk)
		dhPoint, err := curve25519.X25519(sk[:], pkBytes[:])
		if err != nil {
			panic("在加密 sharedSecret 时: " + err.Error()) // XXX: 返回错误/记录日志
		}

		key := crypto.Keccak256(dhPoint)[:16]

		encryptedSharedSecret := encryptedSharedSecret(aesEncryptBlock(key, sharedSecret[:]))
		encryptedSharedSecrets = append(encryptedSharedSecrets, encryptedSharedSecret)
	}

	return SharedSecretEncryptions{
		pkArray,
		common.BytesToHash(crypto.Keccak256(sharedSecret[:])),
		encryptedSharedSecrets,
	}
}

// XXXEncryptSharedSecret 从一组 SharedSecretEncryptionPublicKeys、sharedSecret 和一个密码学随机源构造 SharedSecretEncryptions。
func XXXEncryptSharedSecret(
	keys []types.SharedSecretEncryptionPublicKey,
	sharedSecret *[SharedSecretSize]byte,
	rand io.Reader,
) SharedSecretEncryptions {
	var sk [32]byte
	_, err := io.ReadFull(rand, sk[:])
	if err != nil {
		panic(errors.Wrapf(err, "无法为加密产生熵"))
	}
	return XXXEncryptSharedSecretInternal(keys, sharedSecret, &sk)
}

// 使用 AES-128 加密一个块
func aesEncryptBlock(key, plaintext []byte) [16]byte {
	if len(key) != 16 {
		panic("密钥长度错误")
	}
	if len(plaintext) != 16 {
		panic("明文长度错误")
	}

	cipher, err := aes.NewCipher(key)
	if err != nil {
		// 断言
		panic(fmt.Sprintf("在 aes.NewCipher 过程中出现意外错误: %v", err))
	}

	var ciphertext [16]byte
	cipher.Encrypt(ciphertext[:], plaintext)
	return ciphertext
}
