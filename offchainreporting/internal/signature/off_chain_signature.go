package signature

import (
	"bytes"
	"crypto/ed25519"

	"github.com/pkg/errors"
)



type OffchainPublicKey ed25519.PublicKey


func (k OffchainPublicKey) Equal(k2 OffchainPublicKey) bool {
	return bytes.Equal([]byte(ed25519.PublicKey(k)), []byte(ed25519.PublicKey(k2)))
}


func (k OffchainPublicKey) Verify(msg, signature []byte) bool {
	return ed25519.Verify(ed25519.PublicKey(k), msg, signature)
}



type OffchainPrivateKey ed25519.PrivateKey


func (k *OffchainPrivateKey) Sign(msg []byte) ([]byte, error) {
	if k == nil {
		return nil, errors.Errorf("attempt to sign with nil key")
	}
	return ed25519.Sign(ed25519.PrivateKey(*k), msg), nil
}


func (k *OffchainPrivateKey) PublicKey() OffchainPublicKey {
	return OffchainPublicKey(ed25519.PrivateKey(*k).Public().(ed25519.PublicKey))
}