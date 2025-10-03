package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/tx-blaster/tx-blaster/pkg/models"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(dbPath string) (*Database, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	database := &Database{db: db}
	
	if err := database.createTables(); err != nil {
		return nil, err
	}

	return database, nil
}

func (d *Database) createTables() error {
	// Create table if it doesn't exist
	query := `
	CREATE TABLE IF NOT EXISTS utxos (
		tx_hash TEXT NOT NULL,
		vout INTEGER NOT NULL,
		amount INTEGER NOT NULL,
		block_height INTEGER NOT NULL,
		address TEXT NOT NULL,
		spent BOOLEAN DEFAULT FALSE,
		is_coinbase BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (tx_hash, vout)
	);
	`

	_, err := d.db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}

	// Add is_coinbase column if it doesn't exist (for existing databases)
	_, err = d.db.Exec(`ALTER TABLE utxos ADD COLUMN is_coinbase BOOLEAN DEFAULT FALSE`)
	// Ignore error if column already exists
	
	// Create indexes
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_utxos_address ON utxos(address)`,
		`CREATE INDEX IF NOT EXISTS idx_utxos_spent ON utxos(spent)`,
		`CREATE INDEX IF NOT EXISTS idx_utxos_block_height ON utxos(block_height)`,
		`CREATE INDEX IF NOT EXISTS idx_utxos_coinbase ON utxos(is_coinbase)`,
	}
	
	for _, idx := range indexes {
		if _, err := d.db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

// ValidateUTXO performs basic validation on a UTXO before saving
func (d *Database) ValidateUTXO(utxo *models.UTXO) error {
	// Check transaction hash is valid (64 hex characters)
	if len(utxo.TxHash) != 64 {
		return fmt.Errorf("invalid transaction hash length: %d (expected 64)", len(utxo.TxHash))
	}

	// Check amount is reasonable (not zero unless it's OP_RETURN at vout 0)
	if utxo.Amount == 0 && utxo.Vout != 0 {
		return fmt.Errorf("invalid amount: 0 satoshis for non-OP_RETURN output")
	}

	// Check amount is not suspiciously large (more than 21 million BSV)
	maxSatoshis := uint64(21000000) * uint64(100000000) // 21 million BSV in satoshis
	if utxo.Amount > maxSatoshis {
		return fmt.Errorf("invalid amount: %d satoshis exceeds maximum supply", utxo.Amount)
	}

	// Check block height is reasonable (not in far future)
	currentHeight, err := d.GetCurrentBlockHeight()
	if err == nil && utxo.BlockHeight > currentHeight+10000 {
		return fmt.Errorf("invalid block height: %d is too far in the future (current: %d)",
			utxo.BlockHeight, currentHeight)
	}

	// Check address is not empty
	if utxo.Address == "" {
		return fmt.Errorf("invalid UTXO: address cannot be empty")
	}

	return nil
}

func (d *Database) SaveUTXO(utxo *models.UTXO) error {
	// Validate UTXO before saving
	if err := d.ValidateUTXO(utxo); err != nil {
		return fmt.Errorf("UTXO validation failed: %w", err)
	}

	query := `
	INSERT OR REPLACE INTO utxos (tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	now := time.Now()
	utxo.UpdatedAt = now
	if utxo.CreatedAt.IsZero() {
		utxo.CreatedAt = now
	}

	_, err := d.db.Exec(query, utxo.TxHash, utxo.Vout, utxo.Amount, utxo.BlockHeight,
		utxo.Address, utxo.Spent, utxo.IsCoinbase, utxo.CreatedAt, utxo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save UTXO: %w", err)
	}

	return nil
}

func (d *Database) GetUTXOsByAddress(address string, includeSpent bool) ([]*models.UTXO, error) {
	query := `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ?
	`
	
	if !includeSpent {
		query += " AND spent = FALSE"
	}
	
	query += " ORDER BY block_height DESC, vout"

	rows, err := d.db.Query(query, address)
	if err != nil {
		return nil, fmt.Errorf("failed to query UTXOs: %w", err)
	}
	defer rows.Close()

	var utxos []*models.UTXO
	for rows.Next() {
		utxo := &models.UTXO{}
		err := rows.Scan(&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
			&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan UTXO: %w", err)
		}
		utxos = append(utxos, utxo)
	}

	return utxos, nil
}

func (d *Database) GetUnspentUTXOs(address string) ([]*models.UTXO, error) {
	return d.GetUTXOsByAddress(address, false)
}

func (d *Database) MarkUTXOAsSpent(txHash string, vout uint32) error {
	query := `
	UPDATE utxos 
	SET spent = TRUE, updated_at = ? 
	WHERE tx_hash = ? AND vout = ?
	`

	_, err := d.db.Exec(query, time.Now(), txHash, vout)
	if err != nil {
		return fmt.Errorf("failed to mark UTXO as spent: %w", err)
	}

	return nil
}

func (d *Database) GetTotalBalance(address string) (uint64, error) {
	query := `
	SELECT COALESCE(SUM(amount), 0) 
	FROM utxos 
	WHERE address = ? AND spent = FALSE
	`

	var total uint64
	err := d.db.QueryRow(query, address).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("failed to get total balance: %w", err)
	}

	return total, nil
}

func (d *Database) GetOldestUTXO(address string) (*models.UTXO, error) {
	return d.GetOldestMatureUTXO(address, 0)
}

// GetUTXOByTxHashAndVout retrieves a specific UTXO by transaction hash and output index
func (d *Database) GetUTXOByTxHashAndVout(txHash string, vout uint32) (*models.UTXO, error) {
	query := `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE tx_hash = ? AND vout = ?
	LIMIT 1
	`
	
	utxo := &models.UTXO{}
	err := d.db.QueryRow(query, txHash, vout).Scan(
		&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
		&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
	)
	
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("UTXO not found for tx %s:%d", txHash, vout)
	}
	
	if err != nil {
		return nil, err
	}
	
	return utxo, nil
}

// GetOldestMatureUTXO gets the oldest UTXO that is mature enough to spend
// Prioritizes split/regular UTXOs over coinbase UTXOs
// For coinbase UTXOs, they must be at least 100 blocks old
func (d *Database) GetOldestMatureUTXO(address string, currentBlockHeight uint32) (*models.UTXO, error) {
	// First priority: Try to get split/regular UTXOs (non-coinbase)
	// Only use UTXOs with reasonable block heights (< 100000) to avoid invalid entries
	query := `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ? AND spent = FALSE AND is_coinbase = FALSE AND block_height < 100000
	ORDER BY amount DESC, block_height ASC, vout ASC
	LIMIT 1
	`

	utxo := &models.UTXO{}
	err := d.db.QueryRow(query, address).Scan(
		&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
		&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
	)

	if err == nil {
		// Found a split/regular UTXO, use it
		return utxo, nil
	}

	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get split UTXO: %w", err)
	}

	// Second priority: Try to get oldest mature coinbase UTXO (must be 100+ blocks old)
	// A coinbase UTXO is mature if: currentHeight >= utxo.blockHeight + 100
	query = `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ? AND spent = FALSE AND is_coinbase = TRUE
	AND block_height + 100 <= ?
	ORDER BY block_height ASC, vout ASC
	LIMIT 1
	`

	err = d.db.QueryRow(query, address, currentBlockHeight).Scan(
		&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
		&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
	)

	if err == nil {
		// Found a mature coinbase UTXO
		return utxo, nil
	}

	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get mature coinbase UTXO: %w", err)
	}

	// Last resort: Try any UTXO including immature coinbase
	query = `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ? AND spent = FALSE
	ORDER BY is_coinbase ASC, block_height ASC, vout ASC
	LIMIT 1
	`

	err = d.db.QueryRow(query, address).Scan(
		&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
		&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no unspent UTXOs found for address %s", address)
		}
		return nil, fmt.Errorf("failed to get any UTXO: %w", err)
	}

	return utxo, nil
}

// SaveTransactionOutputs saves all outputs from a transaction as new UTXOs
func (d *Database) SaveTransactionOutputs(txID string, outputs []*models.UTXO) error {
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO utxos (tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	now := time.Now()
	for _, utxo := range outputs {
		utxo.TxHash = txID
		utxo.CreatedAt = now
		utxo.UpdatedAt = now
		
		_, err = stmt.Exec(utxo.TxHash, utxo.Vout, utxo.Amount, utxo.BlockHeight,
			utxo.Address, utxo.Spent, utxo.IsCoinbase, utxo.CreatedAt, utxo.UpdatedAt)
		if err != nil {
			return fmt.Errorf("failed to insert UTXO: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetCurrentBlockHeight returns the highest block height from coinbase UTXOs
func (d *Database) GetCurrentBlockHeight() (uint32, error) {
	query := `
	SELECT MAX(block_height)
	FROM utxos
	WHERE is_coinbase = TRUE
	`
	
	var height sql.NullInt64
	err := d.db.QueryRow(query).Scan(&height)
	if err != nil {
		return 0, fmt.Errorf("failed to get current block height: %w", err)
	}
	
	if !height.Valid {
		return 0, nil
	}
	
	return uint32(height.Int64), nil
}

// GetSmallUTXOs returns all unspent UTXOs below a threshold amount
func (d *Database) GetSmallUTXOs(address string, threshold uint64, limit int) ([]*models.UTXO, error) {
	query := `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ? AND spent = FALSE AND amount < ?
	ORDER BY amount ASC
	LIMIT ?
	`
	
	rows, err := d.db.Query(query, address, threshold, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get small UTXOs: %w", err)
	}
	defer rows.Close()
	
	var utxos []*models.UTXO
	for rows.Next() {
		utxo := &models.UTXO{}
		err := rows.Scan(
			&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
			&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan UTXO: %w", err)
		}
		utxos = append(utxos, utxo)
	}
	
	return utxos, nil
}

// GetAllUnspentUTXOs returns all unspent UTXOs for a given address
func (d *Database) GetAllUnspentUTXOs(address string) ([]*models.UTXO, error) {
	rows, err := d.db.Query(`
		SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
		FROM utxos
		WHERE address = ? AND spent = 0
		ORDER BY block_height ASC, tx_hash ASC, vout ASC
	`, address)
	if err != nil {
		return nil, fmt.Errorf("failed to query unspent UTXOs: %w", err)
	}
	defer rows.Close()
	
	var utxos []*models.UTXO
	for rows.Next() {
		utxo := &models.UTXO{}
		err := rows.Scan(
			&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
			&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan UTXO: %w", err)
		}
		utxos = append(utxos, utxo)
	}
	
	return utxos, nil
}

// DeleteUTXO removes a UTXO from the database
func (d *Database) DeleteUTXO(txHash string, vout uint32) error {
	_, err := d.db.Exec(`
		DELETE FROM utxos WHERE tx_hash = ? AND vout = ?
	`, txHash, vout)
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}