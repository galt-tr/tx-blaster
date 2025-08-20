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
	return NewPropagationBroadcasterWithBatching(propagationGRPCAddr, propagationHTTPAddr, false)
}

// NewPropagationBroadcasterWithBatching creates a new broadcaster with optional batching support
func NewPropagationBroadcasterWithBatching(propagationGRPCAddr string, propagationHTTPAddr string, enableBatching bool) (*PropagationBroadcaster, error) {
	// Create a basic logger
	logger := ulogger.New("tx-blaster")
	
	// Determine batch settings
	batchSize := 0  // No batching by default
	if enableBatching {
		batchSize = 1024  // Maximum batch size for propagation service
	}
	
	// Create settings for the propagation client
	tSettings := &settings.Settings{
		Propagation: settings.PropagationSettings{
			GRPCAddresses:    []string{propagationGRPCAddr},
			HTTPAddresses:    []string{propagationHTTPAddr},
			SendBatchSize:    batchSize,
			SendBatchTimeout: 10,  // 10ms timeout for batching
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

// BroadcastTransactionBatch sends multiple transactions using the internal batcher
// The actual batching is handled by the propagation client
func (pb *PropagationBroadcaster) BroadcastTransactionBatch(txHexes []string) ([]string, []error, error) {
	ctx := context.Background()
	txIDs := make([]string, len(txHexes))
	errors := make([]error, len(txHexes))
	
	// Parse all transactions first
	transactions := make([]*bt.Tx, len(txHexes))
	for i, txHex := range txHexes {
		txBytes, err := hex.DecodeString(txHex)
		if err != nil {
			errors[i] = fmt.Errorf("failed to decode transaction %d: %w", i, err)
			continue
		}
		
		tx, err := bt.NewTxFromBytes(txBytes)
		if err != nil {
			errors[i] = fmt.Errorf("failed to parse transaction %d: %w", i, err)
			continue
		}
		
		transactions[i] = tx
		txIDs[i] = tx.TxID()
	}
	
	// Submit all transactions to the propagation client
	// They will be batched internally by the client
	for i, tx := range transactions {
		if tx == nil {
			continue
		}
		err := pb.client.ProcessTransaction(ctx, tx)
		if err != nil {
			errors[i] = err
		}
	}
	
	// Trigger the batcher to send any pending transactions
	pb.client.TriggerBatcher()
	
	return txIDs, errors, nil
}

// Stop closes the propagation client
func (pb *PropagationBroadcaster) Stop() {
	if pb.client != nil {
		pb.client.Stop()
	}
}