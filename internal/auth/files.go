package auth

import (
	"errors"
	"os"
)

func removeDisabledPassword(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
