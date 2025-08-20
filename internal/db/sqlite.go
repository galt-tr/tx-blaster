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

func (d *Database) SaveUTXO(utxo *models.UTXO) error {
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

// GetOldestMatureUTXO gets the oldest UTXO that is mature enough to spend
// For coinbase UTXOs, they must be at least 100 blocks old
func (d *Database) GetOldestMatureUTXO(address string, currentBlockHeight uint32) (*models.UTXO, error) {
	// First try to get oldest mature coinbase UTXO (100+ blocks old)
	query := `
	SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
	FROM utxos
	WHERE address = ? AND spent = FALSE AND is_coinbase = TRUE
	AND block_height <= ?
	ORDER BY block_height ASC, vout ASC
	LIMIT 1
	`
	
	// Calculate mature height (current - 100 for coinbase maturity)
	matureHeight := int64(currentBlockHeight) - 100
	if matureHeight < 0 {
		matureHeight = 0
	}

	utxo := &models.UTXO{}
	err := d.db.QueryRow(query, address, matureHeight).Scan(
		&utxo.TxHash, &utxo.Vout, &utxo.Amount, &utxo.BlockHeight,
		&utxo.Address, &utxo.Spent, &utxo.IsCoinbase, &utxo.CreatedAt, &utxo.UpdatedAt,
	)
	
	if err != nil {
		if err == sql.ErrNoRows {
			// No coinbase UTXOs found, try any UTXO
			query = `
			SELECT tx_hash, vout, amount, block_height, address, spent, is_coinbase, created_at, updated_at
			FROM utxos
			WHERE address = ? AND spent = FALSE
			ORDER BY block_height ASC, vout ASC
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
				return nil, fmt.Errorf("failed to get oldest UTXO: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to get oldest UTXO: %w", err)
		}
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