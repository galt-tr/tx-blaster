# TX Blaster

BSV Transaction Blaster - A CLI tool for stress testing teranode with split transactions.

## Features

- Scan blockchain for coinbase UTXOs belonging to your address
- Create transactions that split a single UTXO into millions of outputs
- Broadcast transactions via teranode propagation service
- Continuously blast the network with split transactions
- SQLite database for UTXO tracking

## Building

```bash
go build -o tx-blaster cmd/tx-blaster/main.go
```

## Usage

### Scan for coinbase UTXOs

```bash
# Scan last 100 blocks (default)
./tx-blaster scan --key YOUR_WIF_KEY

# Scan all blocks from genesis
./tx-blaster scan --key YOUR_WIF_KEY --all

# Scan specific block range
./tx-blaster scan --key YOUR_WIF_KEY --start 1000 --end 2000
```

### List UTXOs

```bash
./tx-blaster list --key YOUR_WIF_KEY
```

### Create a split transaction

```bash
# Create a transaction that splits oldest UTXO into 1,000 outputs (default)
./tx-blaster split --key YOUR_WIF_KEY

# Create a transaction with custom number of outputs
./tx-blaster split --key YOUR_WIF_KEY --outputs 10000

# Create and immediately broadcast the transaction
./tx-blaster split --key YOUR_WIF_KEY --broadcast
```

### Broadcast a transaction

```bash
# Using propagation service (default)
./tx-blaster broadcast --tx TRANSACTION_HEX

# Using RPC
./tx-blaster broadcast --tx TRANSACTION_HEX --use-propagation=false --rpc http://localhost:9292
```

### Blast continuously

```bash
# Continuously create and broadcast split transactions (default 1000 outputs)
./tx-blaster blast --key YOUR_WIF_KEY

# Blast with custom number of outputs
./tx-blaster blast --key YOUR_WIF_KEY --outputs 10000

# Test mode: Stop after sending a specific number of transactions
./tx-blaster blast --key YOUR_WIF_KEY --iterations 100
```

## Configuration

### Propagation Service

The tool uses teranode's propagation service by default for broadcasting transactions:
- gRPC address: `localhost:8084` (configurable with `--propagation-grpc`)
- HTTP address: `http://localhost:8833` (configurable with `--propagation-http`)

### Teranode Asset Service

For scanning blocks and finding UTXOs:
- Default URL: `http://localhost:8080` (configurable with `--teranode`)

### Database

UTXOs are stored in a local SQLite database:
- Default path: `utxos.db` (configurable with `--database`)

## Transaction Features

### Splitting
The split command takes the oldest mature coinbase UTXO (100+ blocks old) and creates a transaction with many outputs (default 1,000). Each output contains an equal share of the input amount minus a minimal mining fee. All outputs are sent back to the same address.

### Blasting
The blast command runs continuously and:
- Prioritizes mature coinbase UTXOs over split outputs
- Creates 1000 chained transactions from each UTXO for maximum throughput
- **Batch submission**: Sends up to 1000 chained transactions in a single batch
- Periodically syncs new coinbase UTXOs every 5 minutes
- Reports statistics every 30 seconds (TPS, success rate, etc.)
- Ensures long-term sustainability by preserving funds

When using the propagation service (default), chained transactions are submitted as a complete batch to ensure proper ordering. The transactions are processed sequentially to maintain the chain dependency.

### Transaction Tracking
All transactions include an OP_RETURN output with the message "Who is John Galt?" making them easily identifiable on the network.

### Coinbase Maturity
The system automatically enforces the 100-block maturity requirement for coinbase UTXOs, ensuring transactions are valid.