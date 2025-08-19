package keys

import (
	"fmt"

	primitives "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
)

type KeyManager struct {
	privateKey *primitives.PrivateKey
	address    string
	network    Network
}

type Network string

const (
	Mainnet Network = "mainnet"
	Testnet Network = "testnet"
)

func NewKeyManager(wifKey string) (*KeyManager, error) {
	privKey, err := primitives.PrivateKeyFromWif(wifKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse WIF key: %w", err)
	}

	// Determine network based on WIF prefix
	network := Mainnet
	if wifKey[0] == 'c' || wifKey[0] == '9' {
		network = Testnet
	}

	km := &KeyManager{
		privateKey: privKey,
		network:    network,
	}

	// Generate address
	if err := km.generateAddress(); err != nil {
		return nil, err
	}

	return km, nil
}

func (km *KeyManager) generateAddress() error {
	pubKey := km.privateKey.PubKey()
	
	// Create address from public key
	isMainnet := km.network == Mainnet
	addr, err := script.NewAddressFromPublicKey(pubKey, isMainnet)
	if err != nil {
		return fmt.Errorf("failed to create address: %w", err)
	}

	km.address = addr.AddressString

	return nil
}

func (km *KeyManager) GetAddress() string {
	return km.address
}

func (km *KeyManager) GetPrivateKey() *primitives.PrivateKey {
	return km.privateKey
}

func (km *KeyManager) GetNetwork() Network {
	return km.network
}

func (km *KeyManager) IsTestnet() bool {
	return km.network == Testnet
}