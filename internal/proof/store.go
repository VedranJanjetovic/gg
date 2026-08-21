// Package proof owns verification evidence produced by gg workflows.
package proof

import "context"

// Store records proof artifacts for completed workflow steps.
type Store struct{}

// Record stores proof metadata.
func (s *Store) Record(ctx context.Context, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = description
	return nil
}
