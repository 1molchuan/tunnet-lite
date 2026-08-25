// Package updater polls the control plane for node changes.
//
// It deliberately does not act on what it finds. Applying a new directory means
// rebuilding the tunnel, which drops every connection in flight; deciding when
// that is acceptable belongs to whoever is using the proxy, not to a timer.
package updater

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/control"
	"github.com/1molchuan/tunnet-lite/internal/inventory"
)

// Updater refreshes the inventory on an interval and reports what moved.
type Updater struct {
	Session  *control.Session
	Interval time.Duration

	// OnUpdate is called only when something actually changed. It runs on the
	// updater's own goroutine, so it must be safe to call concurrently with
	// whatever else holds the inventory.
	OnUpdate func(*inventory.Inventory, inventory.Changes)
}

// Run polls until ctx is done. A poll that fails is reported and retried at the
// next tick: a control plane that is briefly unreachable is not a reason to
// stop watching.
func (u *Updater) Run(ctx context.Context, current *inventory.Inventory) {
	if u.Session == nil || u.Interval <= 0 {
		return
	}
	log.Printf("watching for node changes every %s", u.Interval)

	ticker := time.NewTicker(u.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		next, err := u.Session.Refresh(ctx, control.RefreshOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var needsAuth *control.NeedsAuthorizationError
			if errors.As(err, &needsAuth) {
				log.Printf("node refresh needs authorisation; approve at %s", needsAuth.VerificationURL)
				continue
			}
			log.Printf("node refresh failed: %v", err)
			continue
		}

		changes := inventory.Compare(current, next)
		current = next
		if changes.Empty() {
			continue
		}
		log.Printf("nodes changed — %s", changes)
		log.Printf("the running tunnel still uses the previous set; apply it when convenient")
		if u.OnUpdate != nil {
			u.OnUpdate(next, changes)
		}
	}
}
