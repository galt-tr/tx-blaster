# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

TX Blaster is a BSV blockchain stress testing tool that creates and broadcasts large volumes of chained transactions to test teranode performance. It tracks UTXOs from coinbase transactions, creates chains of 1000+ transactions, and submits them via batch gRPC calls.

## Build and Run Commands

```bash
# Build the application
go build -o tx-blaster cmd/tx-blaster/*.go

# Or build all commands
go build -o tx-blaster cmd/tx-blaster/main.go

# Run tests (if any)
go test ./...

# Run with help
./tx-blaster --help

# Common development workflow
./tx-blaster scan --key YOUR_WIF_KEY        # Scan for UTXOs
./tx-blaster list --key YOUR_WIF_KEY        # List found UTXOs  
./tx-blaster blast --key YOUR_WIF_KEY       # Start blasting transactions
```

## Architecture

### Transaction Strategy
The blaster creates **chained transactions** where each transaction spends the output of the previous one. This allows generating 1000 transactions from a single UTXO:
- Transaction 1 spends original UTXO → creates output A
- Transaction 2 spends output A → creates output B  
- Transaction 3 spends output B → creates output C
- ... continues for 1000 transactions

All 1000 chained transactions are submitted in a single batch gRPC call for efficiency.

### Key Components

**Command Layer** (`cmd/tx-blaster/`)
- `main.go` - CLI entry point using Cobra
- `blast.go` - BlastManager orchestrates continuous transaction creation/broadcasting
- `verify.go` - Verifies UTXOs against teranode/aerospike

**Transaction Building** (`internal/blaster/`)
- `simple_builder.go` - Creates chained transactions (`BuildManyTransactions`)
- `builder.go` - Creates split transactions with many outputs
- `batch_builder.go` - Optimizes for large split transactions

**Broadcasting** (`internal/broadcaster/`)
- `batch_propagation.go` - DirectBatchBroadcaster uses gRPC `ProcessTransactionBatch` API
- `propagation.go` - PropagationBroadcaster uses teranode propagation client
- `broadcaster.go` - Basic RPC broadcaster

**Data Layer** (`internal/db/`)
- SQLite database tracks UTXOs with `is_coinbase` flag
- Prioritizes coinbase UTXOs (100+ blocks old for maturity)
- Stores: tx_hash, vout, amount, block_height, address, spent, is_coinbase

### Teranode Integration

The project has a local replace directive for teranode:
```go
replace github.com/bitcoin-sv/teranode => /git/teranode
```

Key teranode services used:
- **Asset HTTP API** (`http://localhost:8080`) - Block/UTXO queries
- **Propagation gRPC** (`localhost:8084`) - Batch transaction submission via `ProcessTransactionBatch`
- **Propagation HTTP** (`http://localhost:8833`) - Fallback for large transactions

### Transaction Features

1. **OP_RETURN Tracking**: All transactions include `OP_RETURN "Who is John Galt?"` for identification
2. **Coinbase Maturity**: Enforces 100-block maturity for coinbase UTXOs
3. **Batch Submission**: Sends up to 1000 transactions in one gRPC call
4. **Chain Preservation**: Last transaction output saved as new UTXO for next batch

## Important Configuration

Default ports and endpoints:
- Teranode Asset API: `http://localhost:8080`
- Propagation gRPC: `localhost:8084`  
- Propagation HTTP: `http://localhost:8833`
- RPC (fallback): `http://localhost:9292`
- Database: `utxos.db` (SQLite)

## Critical Implementation Details

### Batch Transaction Submission
The `DirectBatchBroadcaster` in `batch_propagation.go` directly calls the gRPC `ProcessTransactionBatch` method. This is crucial for performance - it sends all 1000 chained transactions in a single network call rather than queuing them individually.

### UTXO Management
- Coinbase UTXOs are tracked with `is_coinbase=true`
- `GetOldestMatureUTXO` prioritizes coinbase UTXOs first
- UTXOs marked spent only after successful broadcast
- Block height stored as `current + 1000000` for new UTXOs to prevent immediate reuse

### Error Handling
- TX_MISSING_PARENT errors indicate transaction dependency issues
- Batch failures don't mark UTXOs as spent
- Individual transaction errors tracked separately in batch response

## Common Issues and Solutions

1. **TX_MISSING_PARENT**: Transactions have dependencies not yet in mempool
   - Solution: Ensure all chained transactions submitted in single batch
   
2. **Slow batch submission**: Using wrong API or sequential submission
   - Solution: Use DirectBatchBroadcaster for direct gRPC batch calls

3. **Database migration errors**: Adding columns to existing database
   - Solution: Migration code suppresses "column already exists" errors