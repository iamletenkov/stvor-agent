package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/iamletenkov/stvor-agent/internal/inventory"
)

// wireInventory is the body of PUT /inventory: the hash first, the body only
// when the portal answers 409 need_body.
type wireInventory struct {
	Schema int    `json:"schema"`
	Hash   string `json:"hash"`
	wireSender
	Body json.RawMessage `json:"body,omitempty"`
}

// inventoryHandler: nothing to ack; a body over the limit cannot be split.
type inventoryHandler struct{ s *Sender }

func (inventoryHandler) ack() error    { return nil }
func (inventoryHandler) backlog() bool { return false }
func (h inventoryHandler) tooLarge(status int) Result {
	h.s.opt.Log.Warn("портал не принял инвентарь по размеру", "event", "telemetry.inventory", "status", status)
	return Result{Outcome: OutcomeDropped, HTTPStatus: status}
}

// SyncInventory sends the hash of body (without collected_at) and, when the
// portal does not know it, the body itself. OutcomeMatch — the portal has it.
func (s *Sender) SyncInventory(ctx context.Context, body inventory.Body) Result {
	s.send.Lock()
	defer s.send.Unlock()
	now, start := s.opt.Now(), s.opt.MonoMS()
	cr, res, ok := s.credentials(now)
	if !ok {
		return res
	}
	hash, err := inventory.Hash(body)
	if err != nil {
		return Result{Outcome: OutcomeDropped, Err: fmt.Errorf("хэш инвентаря: %w", err)}
	}
	w := wireInventory{Schema: s.k.Schema, Hash: hash}
	a, err := s.putInventory(ctx, cr, w)
	if err != nil {
		return Result{Outcome: OutcomeDropped, Err: err}
	}
	if a.status == http.StatusConflict && replyCode(a.body) == codeNeedBody {
		full, err := inventory.Encode(body)
		switch {
		case errors.Is(err, inventory.ErrTooLarge):
			s.opt.Log.Warn("инвентарь больше лимита контракта — не отправлен", "event", "telemetry.inventory")
			return Result{Outcome: OutcomeDropped, Err: err}
		case err != nil:
			return Result{Outcome: OutcomeDropped, Err: fmt.Errorf("инвентарь: %w", err)}
		}
		w.Body = full
		if a, err = s.putInventory(ctx, cr, w); err != nil {
			return Result{Outcome: OutcomeDropped, Err: err}
		}
	}
	return s.settle(a, cr, start, now, inventoryHandler{s: s})
}

func (s *Sender) putInventory(ctx context.Context, cr Credentials, w wireInventory) (answer, error) {
	if err := s.pace(ctx); err != nil {
		return answer{}, err
	}
	w.wireSender = s.senderIdentity() // sent_mono_ms of this very attempt
	raw, err := json.Marshal(w)
	if err != nil {
		return answer{}, fmt.Errorf("инвентарь: %w", err)
	}
	gz, err := s.gz.gzip(raw)
	if err != nil {
		return answer{}, err
	}
	if len(gz) > s.k.Limits.BodyGzipMax {
		return answer{}, fmt.Errorf("инвентарь: gzip %d байт", len(gz))
	}
	return s.request(ctx, http.MethodPut, cr, "/inventory", gz, s.opt.Now()), nil
}
