//go:build windows

package robustio

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// A scanner releases its handle within a few milliseconds; the budget is
// generous enough to outlast a loaded CI runner and short enough that a
// genuine permission error still surfaces promptly.
const (
	retryBudget = time.Second
	initialGap  = time.Millisecond
	maxGap      = 50 * time.Millisecond
)

// retry repeats the operation while it fails with a sharing error, until it
// succeeds, fails for another reason, or the budget expires.
func retry(operation func() error) error {
	deadline := time.Now().Add(retryBudget)
	gap := initialGap
	for {
		err := operation()
		if err == nil || !sharingFailure(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(gap)
		if gap < maxGap {
			gap *= 2
		}
	}
}

// sharingFailure reports whether Windows refused the operation because another
// handle to the file is open, which is the failure mode that clears on its own.
func sharingFailure(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
