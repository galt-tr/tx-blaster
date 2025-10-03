package broadcaster

import "fmt"

// BatchBroadcasterWrapper wraps DirectBatchBroadcaster to provide single transaction broadcast
type BatchBroadcasterWrapper struct {
	DB *DirectBatchBroadcaster
}

func (b *BatchBroadcasterWrapper) BroadcastTransaction(txHex string) (string, error) {
	txIDs, errs, err := b.DB.BroadcastTransactionBatch([]string{txHex})
	if err != nil {
		return "", err
	}
	if len(errs) > 0 && errs[0] != nil {
		return "", errs[0]
	}
	if len(txIDs) > 0 {
		return txIDs[0], nil
	}
	return "", fmt.Errorf("no transaction ID returned")
}