package buffer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

const (
	eventsFile     = "events.json"
	maxEventsBytes = 16 << 20
)

// Event is an agent event (contract events.event). Events are never rolled
// up and go to disk at once: they are rare and outlive metrics under quota.
type Event struct {
	ID          string         `json:"event_id"`
	At          time.Time      `json:"at"`
	MonoMS      int64          `json:"mono_ms"`
	BootID      string         `json:"boot_id"`
	ClockSynced bool           `json:"clock_synced"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	Params      map[string]any `json:"params,omitempty"`
}

// AddEvent stores an event. An empty ID gets a ULID; empty MonoMS and BootID
// come from the buffer clock and the last record.
func (b *Buffer) AddEvent(e Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	return b.addEvent(e)
}

func (b *Buffer) addEvent(e Event) error {
	if e.At.IsZero() {
		e.At = b.cfg.Now()
	}
	if e.ID == "" {
		id, err := newULID(e.At, b.cfg.Rand)
		if err != nil {
			return err
		}
		e.ID = id
	}
	if e.MonoMS == 0 {
		e.MonoMS = b.cfg.MonoMS()
	}
	if e.BootID == "" {
		e.BootID, e.ClockSynced = b.lastBoot, b.synced
	}
	e.Params = jsonParams(e.Params)
	b.events = append(b.events, e)
	return b.saveEvents()
}

// jsonParams drops the params JSON cannot carry (NaN, Inf, channels…): one
// such value would make events.json unwritable for every later event.
func jsonParams(p map[string]any) map[string]any {
	var out map[string]any
	for k, v := range p {
		if _, err := json.Marshal(v); err != nil {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(p))
		}
		out[k] = v
	}
	return out
}

func (b *Buffer) saveEvents() error {
	path := filepath.Join(b.cfg.Dir, eventsFile)
	if len(b.events) == 0 {
		b.evSize = 0
		return b.removeFile(eventsFile)
	}
	data, err := json.Marshal(b.events)
	for err == nil && len(data) > maxEventsBytes && len(b.events) > 1 {
		// Keep the tail: a file over the read limit would go to quarantine
		// whole on the next start.
		drop := max(1, len(b.events)*(len(data)-maxEventsBytes)/len(data))
		b.events, b.dropped = b.events[drop:], b.dropped+drop
		b.inflightEventsGone()
		data, err = json.Marshal(b.events)
	}
	if err != nil {
		return err
	}
	if err := b.faultAt("write", eventsFile); err != nil {
		return err
	}
	if err := (atomicfile.OS{}).WriteAtomic(path, data, 0o600); err != nil {
		return err
	}
	b.evSize = int64(len(data))
	return nil
}

// loadEvents reads the events file; a damaged one goes to quarantine.
func (b *Buffer) loadEvents() error {
	data, _, err := (atomicfile.OS{}).ReadNoFollow(filepath.Join(b.cfg.Dir, eventsFile), maxEventsBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err == nil && json.Unmarshal(data, &b.events) == nil:
		b.evSize = int64(len(data))
		return nil
	}
	b.events = nil
	return b.quarantineFile(eventsFile, "corrupted")
}
