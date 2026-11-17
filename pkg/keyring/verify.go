package keyring

import (
	"bytes"
	"errors"
	"fmt"
)

// verifyWrite checks the store that agy will actually read. A successful file
// fallback must not hide an unchanged credential in a higher-priority keyring.
func verifyWrite(store TokenStore, data []byte, primaryErr, mirrorErr error) error {
	if mirrorErr != nil {
		return errors.Join(primaryErr, mirrorErr)
	}
	actual, err := store.ReadLive()
	if err != nil {
		return errors.Join(primaryErr, fmt.Errorf("verify credential write: %w", err))
	}
	if !bytes.Equal(actual, data) {
		return errors.Join(primaryErr, errors.New("credential store still contains the previous login"))
	}
	return nil
}
