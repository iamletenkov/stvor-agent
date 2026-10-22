package telemetry

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"

	"github.com/iamletenkov/stvor-agent/internal/buffer"
)

// wireEvents is the body of POST /events; the portal is idempotent by
// (robot, event_id), so a retry never duplicates.
type wireEvents struct {
	Schema int `json:"schema"`
	wireSender
	Events []wireEvent `json:"events"`
}

// wireSender is the sender identity of PUT /inventory and POST /events
// (contract v1): the same machine_id_hash and boot_id as the metrics batches
// of this boot and CLOCK_BOOTTIME of this attempt — all three or none (a part
// is a 400 telemetry.invalid).
type wireSender struct {
	MachineIDHash string `json:"machine_id_hash,omitempty"`
	BootID        string `json:"boot_id,omitempty"`
	SentMonoMS    *int64 `json:"sent_mono_ms,omitempty"`
}

// reBootID is the boot_id pattern of the contract.
var reBootID = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// senderIdentity is the identity of this attempt; zero when the machine-id
// hash or the boot is not known.
func (s *Sender) senderIdentity() wireSender {
	m := s.opt.Meta()
	if !reSHA256.MatchString(m.MachineIDHash) || !reBootID.MatchString(m.BootID) {
		return wireSender{}
	}
	mono := s.opt.MonoMS()
	return wireSender{MachineIDHash: m.MachineIDHash, BootID: m.BootID, SentMonoMS: &mono}
}

type wireEvent struct {
	ID          string          `json:"event_id"`
	At          string          `json:"at"`
	MonoMS      int64           `json:"mono_ms"`
	BootID      string          `json:"boot_id"`
	ClockSynced bool            `json:"clock_synced"`
	Kind        string          `json:"kind"`
	Severity    string          `json:"severity"`
	Params      json.RawMessage `json:"params"`
}

// eventsBody builds the body of an events chunk. Events are checked against
// events.kinds of the contract before they leave: an unknown kind is dropped,
// a severity outside info|warning|danger takes the kind's own, params keep
// only the keys of kinds[].params with string, finite number or bool values;
// params over event_params_max_bytes go as {} (the event itself is kept). A
// chunk with no valid event left gives raw == nil: the caller acks it.
func (s *Sender) eventsBody(c *buffer.Chunk) (raw []byte, over string, err error) {
	if len(c.Events) > s.k.Limits.EventsPerRequest {
		return nil, fmt.Sprintf("%d событий", len(c.Events)), nil
	}
	w := wireEvents{Schema: s.k.Schema, wireSender: s.senderIdentity(), Events: make([]wireEvent, 0, len(c.Events))}
	for _, e := range c.Events {
		kind, ok := s.k.Events[e.Kind]
		if !ok {
			s.opt.Log.Warn("событие вида вне контракта — отброшено", "event", "telemetry.dropped",
				"kind", quoteCut(e.Kind))
			continue
		}
		sev := e.Severity
		if sev != "info" && sev != "warning" && sev != "danger" {
			sev = kind.Severity
		}
		w.Events = append(w.Events, wireEvent{
			ID: e.ID, At: rfc3339(e.At), MonoMS: e.MonoMS, BootID: e.BootID, ClockSynced: e.ClockSynced,
			Kind: e.Kind, Severity: sev, Params: s.eventParams(e, kind),
		})
	}
	if len(w.Events) == 0 {
		return nil, "", nil
	}
	if raw, err = json.Marshal(w); err != nil {
		return nil, "", fmt.Errorf("события: %w", err)
	}
	if len(raw) > s.k.Limits.BodyInflatedMax {
		return nil, fmt.Sprintf("%d байт без сжатия", len(raw)), nil
	}
	return raw, "", nil
}

// eventParams keeps the allowed params of an event; anything that does not
// serialize goes as {} with a journal line — never an endless retry.
func (s *Sender) eventParams(e buffer.Event, kind EventKind) json.RawMessage {
	empty := json.RawMessage(`{}`)
	if len(e.Params) == 0 {
		return empty
	}
	keep := map[string]any{}
	for _, k := range kind.Params {
		v, ok := e.Params[k]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case string, bool:
			keep[k] = x
		case float64:
			if !math.IsNaN(x) && !math.IsInf(x, 0) {
				keep[k] = x
			}
		case float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			keep[k] = x
		}
	}
	if len(keep) != len(e.Params) {
		s.opt.Log.Debug("параметры события вне контракта отброшены", "event", "telemetry.event",
			"kind", e.Kind, "dropped", len(e.Params)-len(keep))
	}
	p, err := json.Marshal(keep)
	if err != nil {
		s.opt.Log.Warn("параметры события не сериализуются — отправлены пустыми", "event", "telemetry.event",
			"kind", e.Kind, "err", err)
		return empty
	}
	if len(p) > s.k.Limits.EventParamsMax {
		return empty
	}
	return p
}
