package broadcaster

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/bitcoin-sv/teranode/services/propagation"
	"github.com/bitcoin-sv/teranode/settings"
	"github.com/bitcoin-sv/teranode/ulogger"
	"github.com/bsv-blockchain/go-bt/v2"
)

type PropagationBroadcaster struct {
	client *propagation.Client
	logger ulogger.Logger
}

// NewPropagationBroadcaster creates a new broadcaster using the teranode propagation client
func NewPropagationBroadcaster(propagationGRPCAddr string, propagationHTTPAddr string) (*PropagationBroadcaster, error) {
	// Create a basic logger
	logger := ulogger.New("tx-blaster")
	
	// Create settings for the propagation client
	tSettings := &settings.Settings{
		Propagation: settings.PropagationSettings{
			GRPCAddresses:    []string{propagationGRPCAddr},
			HTTPAddresses:    []string{propagationHTTPAddr},
			SendBatchSize:    0, // Disable batching for now
			SendBatchTimeout: 5,
			AlwaysUseHTTP:    false,
		},
	}
	
	// Create the propagation client
	ctx := context.Background()
	client, err := propagation.NewClient(ctx, logger, tSettings)
	if err != nil {
		return nil, fmt.Errorf("failed to create propagation client: %w", err)
	}
	
	return &PropagationBroadcaster{
		client: client,
		logger: logger,
	}, nil
}

// BroadcastTransaction sends a transaction using the teranode propagation client
func (pb *PropagationBroadcaster) BroadcastTransaction(txHex string) (string, error) {
	// Decode the transaction hex
	txBytes, err := hex.DecodeString(txHex)
	if err != nil {
		return "", fmt.Errorf("failed to decode transaction hex: %w", err)
	}
	
	// Parse the transaction
	tx, err := bt.NewTxFromBytes(txBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse transaction: %w", err)
	}
	
	// Process the transaction through the propagation client
	ctx := context.Background()
	err = pb.client.ProcessTransaction(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("failed to process transaction: %w", err)
	}
	
	// Return the transaction ID
	return tx.TxID(), nil
}

// Stop closes the propagation client
func (pb *PropagationBroadcaster) Stop() {
	if pb.client != nil {
		pb.client.Stop()
	}
}