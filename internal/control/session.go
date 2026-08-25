package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/inventory"
)

// NeedsAuthorizationError reports that the identity exists but has not been
// approved yet. VerificationURL is the page a human opens to approve it.
type NeedsAuthorizationError struct {
	Ticket          string
	VerificationURL string
}

func (e *NeedsAuthorizationError) Error() string {
	if e.VerificationURL != "" {
		return "this client is not authorised yet; approve it at " + e.VerificationURL
	}
	return "this client is not authorised yet and no verification URL was returned"
}

// Session ties a persisted identity to the control-plane calls and produces an
// inventory. An authorised identity refreshes with a plain sync; a new one has
// to bootstrap and be approved first.
type Session struct {
	IdentityPath  string
	InventoryPath string
	Client        *Client
}

type RefreshOptions struct {
	// AutoApprove calls the verification endpoint directly instead of waiting
	// for a human to open the page. Only meaningful for your own account.
	AutoApprove bool
	// VerificationURL overrides the URL discovered in the bootstrap payload.
	VerificationURL string
}

// Open loads the stored identity, creating and persisting a new one if none
// exists. The returned bool reports whether the identity is newly created.
func Open(identityPath, inventoryPath string) (*Session, bool, error) {
	id, err := LoadIdentity(identityPath)
	fresh := false
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if id, err = NewIdentity(); err != nil {
			return nil, false, err
		}
		if err := id.Save(identityPath); err != nil {
			return nil, false, fmt.Errorf("persist identity: %w", err)
		}
		fresh = true
	default:
		return nil, false, err
	}
	return &Session{
		IdentityPath:  identityPath,
		InventoryPath: inventoryPath,
		Client:        NewClient(id),
	}, fresh, nil
}

// Refresh fetches a directory and writes it to InventoryPath.
//
// An authorised identity is refreshed with sync. Anything else falls back to
// bootstrap, which either yields a ticket to approve or, once approved, an
// access payload.
func (s *Session) Refresh(ctx context.Context, o RefreshOptions) (*inventory.Inventory, error) {
	inv, syncErr := s.trySync(ctx)
	if syncErr == nil {
		return inv, s.persist(inv)
	}

	boot, err := s.Client.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	if boot.Ticket == "" {
		// The identity is known but sync did not yield a directory, so the sync
		// failure is the real problem rather than a missing authorisation.
		return nil, fmt.Errorf("no directory available (bootstrap state %q); sync failed: %w", boot.State, syncErr)
	}

	verifyURL := o.VerificationURL
	if verifyURL == "" {
		verifyURL = VerificationURL(boot.Raw)
	}
	if !o.AutoApprove {
		return nil, &NeedsAuthorizationError{Ticket: boot.Ticket, VerificationURL: verifyURL}
	}
	if err := s.Client.Authorize(ctx, verifyURL, boot.Ticket); err != nil {
		return nil, err
	}
	if boot.RetryAfter > 0 {
		select {
		case <-time.After(boot.RetryAfter):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	payload, err := s.Client.Access(ctx, boot.Ticket)
	if err != nil {
		return nil, err
	}
	inv, err = ParseInventory(payload)
	if err != nil {
		return nil, err
	}
	return inv, s.persist(inv)
}

// Complete finishes an authorisation that a human approved out of band.
func (s *Session) Complete(ctx context.Context, ticket string) (*inventory.Inventory, error) {
	payload, err := s.Client.Access(ctx, ticket)
	if err != nil {
		return nil, err
	}
	inv, err := ParseInventory(payload)
	if err != nil {
		return nil, err
	}
	return inv, s.persist(inv)
}

// trySync refreshes an already authorised identity. A sync response omits the
// root-domain pool, so whatever is already on disk is merged in before the
// result is validated.
func (s *Session) trySync(ctx context.Context) (*inventory.Inventory, error) {
	payload, err := s.Client.Sync(ctx)
	if err != nil {
		return nil, err
	}
	inv, err := s.parseSync(payload)
	if err == nil {
		return inv, nil
	}

	// A client below the advertised floor is answered with the release block
	// and nothing else. The floor is in that answer, so adopt it and ask again
	// rather than reporting a missing runtime section.
	floor := MinimumVersion(payload)
	if floor == "" || !versionLess(s.Client.AppVersion, floor) {
		return nil, err
	}
	log.Printf("control plane requires client version %s; reporting it and retrying", floor)
	s.Client.AppVersion = floor

	payload, retryErr := s.Client.Sync(ctx)
	if retryErr != nil {
		return nil, retryErr
	}
	return s.parseSync(payload)
}

func (s *Session) parseSync(payload []byte) (*inventory.Inventory, error) {
	inv, err := ParseDirectory(payload)
	if err != nil {
		return nil, err
	}
	if prev, err := inventory.Load(s.InventoryPath); err == nil {
		inv.FillMissingFrom(prev)
	}
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("sync directory is not usable: %w", err)
	}
	return inv, nil
}

func (s *Session) persist(inv *inventory.Inventory) error {
	if s.InventoryPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.InventoryPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return err
	}
	// The inventory carries the account credential and node keys.
	return os.WriteFile(s.InventoryPath, append(data, '\n'), 0o600)
}
