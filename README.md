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

## Transaction Splitting

The split command takes the oldest unspent UTXO and creates a transaction with many outputs (default 1,000). Each output contains an equal share of the input amount minus a minimal mining fee (1 satoshi per byte, minimum 1 satoshi total). All outputs are sent back to the same address. This creates transactions ideal for stress testing with maximum efficiency.

After broadcasting a split transaction, all new UTXOs are automatically saved to the database for use in subsequent blasting operations.