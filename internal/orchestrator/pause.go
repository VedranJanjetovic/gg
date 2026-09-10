package orchestrator

import "errors"

// pauseError parks a run instead of failing it: the pipeline stopped on a
// condition that needs a human decision — an unclassifiable verification
// check, an exhausted retry budget — not on broken work or broken agent
// infrastructure. closeFailedRun answers it by closing the run as stopped
// with a durable pause record instead of failed.
type pauseError struct {
	reason     string
	nextAction string
	cause      error
}

func (e *pauseError) Error() string {
	if e == nil || e.cause == nil {
		if e != nil && e.reason != "" {
			return e.reason
		}
		return "run paused for external resolution"
	}
	return e.cause.Error()
}

func (e *pauseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func isPause(err error) bool {
	var pause *pauseError
	return errors.As(err, &pause)
}
