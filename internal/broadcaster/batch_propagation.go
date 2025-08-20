package broadcaster

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/bitcoin-sv/teranode/services/propagation/propagation_api"
	"github.com/bitcoin-sv/teranode/ulogger"
	"github.com/bsv-blockchain/go-bt/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DirectBatchBroadcaster uses the propagation service's batch API directly
type DirectBatchBroadcaster struct {
	client propagation_api.PropagationAPIClient
	conn   *grpc.ClientConn
	logger ulogger.Logger
}

// NewDirectBatchBroadcaster creates a broadcaster that uses the batch API directly
func NewDirectBatchBroadcaster(propagationGRPCAddr string) (*DirectBatchBroadcaster, error) {
	logger := ulogger.New("tx-blaster-batch")
	
	// Create gRPC connection
	conn, err := grpc.Dial(propagationGRPCAddr, 
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(100*1024*1024)), // 100MB max message size
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to propagation service: %w", err)
	}
	
	client := propagation_api.NewPropagationAPIClient(conn)
	
	return &DirectBatchBroadcaster{
		client: client,
		conn:   conn,
		logger: logger,
	}, nil
}

// BroadcastTransactionBatch sends all transactions in a single batch gRPC call
func (db *DirectBatchBroadcaster) BroadcastTransactionBatch(txHexes []string) ([]string, []error, error) {
	ctx := context.Background()
	txIDs := make([]string, len(txHexes))
	errors := make([]error, len(txHexes))
	
	// Parse all transactions and create batch items
	items := make([]*propagation_api.BatchTransactionItem, len(txHexes))
	for i, txHex := range txHexes {
		txBytes, err := hex.DecodeString(txHex)
		if err != nil {
			errors[i] = fmt.Errorf("failed to decode transaction %d: %w", i, err)
			return txIDs, errors, fmt.Errorf("failed to decode transaction %d", i)
		}
		
		// Parse to get TX ID
		tx, err := bt.NewTxFromBytes(txBytes)
		if err != nil {
			errors[i] = fmt.Errorf("failed to parse transaction %d: %w", i, err)
			return txIDs, errors, fmt.Errorf("failed to parse transaction %d", i)
		}
		txIDs[i] = tx.TxID()
		
		// Create batch item with raw bytes
		items[i] = &propagation_api.BatchTransactionItem{
			Tx:           txBytes,
			TraceContext: make(map[string]string), // Empty trace context
		}
	}
	
	// Create batch request
	request := &propagation_api.ProcessTransactionBatchRequest{
		Items: items,
	}
	
	// Send the entire batch in one gRPC call
	db.logger.Infof("Sending batch of %d transactions via gRPC", len(items))
	response, err := db.client.ProcessTransactionBatch(ctx, request)
	if err != nil {
		// If the batch call fails, all transactions fail
		for i := range errors {
			errors[i] = fmt.Errorf("batch submission failed: %w", err)
		}
		return txIDs, errors, err
	}
	
	// Process the response - each transaction gets its own error status
	if response.Errors != nil {
		for i, tErr := range response.Errors {
			if tErr != nil && !tErr.IsNil() {
				errors[i] = fmt.Errorf("transaction %d failed: %s", i, tErr.String())
			}
		}
	}
	
	return txIDs, errors, nil
}

// BroadcastTransaction sends a single transaction
func (db *DirectBatchBroadcaster) BroadcastTransaction(txHex string) (string, error) {
	txIDs, errs, err := db.BroadcastTransactionBatch([]string{txHex})
	if err != nil {
		return "", err
	}
	if errs[0] != nil {
		return "", errs[0]
	}
	return txIDs[0], nil
}

// Stop closes the gRPC connection
func (db *DirectBatchBroadcaster) Stop() {
	if db.conn != nil {
		db.conn.Close()
	}
}