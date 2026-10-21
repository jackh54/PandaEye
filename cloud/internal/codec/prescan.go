package codec

import "google.golang.org/protobuf/encoding/protowire"

// Field numbers walked by the pre-scan (wire.proto).
const (
	fClientFrameBatch    = 2
	fBatchEvents         = 4
	fEventAttack         = 13
	fEventFlag           = 20
	fEventState          = 23
	fAttackTargetHistory = 11
	fFlagData            = 7
	fFlagContext         = 9
	fStateEffects        = 12
)

// prescan bounds what proto.Unmarshal would allocate for a ClientFrame: the number of Batch.events
// (summed over repeated batch fields, which protobuf merges), the size of every event, and the
// repeated fields inside events that could otherwise multiply allocations (target_history, LocalFlag
// data and context, StateChange effects; summed over merged occurrences). It also rejects malformed
// wire data early.
func prescan(b []byte, lim Limits) error {
	events := 0
	return walk(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if num != fClientFrameBatch || typ != protowire.BytesType {
			return nil
		}
		return walk(v, func(num protowire.Number, typ protowire.Type, ev []byte) error {
			if num != fBatchEvents || typ != protowire.BytesType {
				return nil
			}
			events++
			if events > lim.MaxBatchEvents {
				return tooLarge("batch has more than %d events", lim.MaxBatchEvents)
			}
			if len(ev) > lim.MaxEventBytes {
				return tooLarge("event of %d bytes exceeds %d", len(ev), lim.MaxEventBytes)
			}
			return scanEvent(ev)
		})
	})
}

func scanEvent(b []byte) error {
	var history, data, context, effects int
	return walk(b, func(num protowire.Number, typ protowire.Type, v []byte) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case fEventAttack:
			return countField(v, fAttackTargetHistory, &history, MaxTargetHistory, "target_history")
		case fEventFlag:
			if err := countField(v, fFlagData, &data, MaxDataEntries, "flag data"); err != nil {
				return err
			}
			return countField(v, fFlagContext, &context, MaxContextMoves, "flag context")
		case fEventState:
			return countField(v, fStateEffects, &effects, MaxEffects, "effects")
		}
		return nil
	})
}

func countField(b []byte, field protowire.Number, n *int, max int, name string) error {
	return walk(b, func(num protowire.Number, _ protowire.Type, _ []byte) error {
		if num == field {
			*n++
			if *n > max {
				return tooLarge("%s has more than %d entries", name, max)
			}
		}
		return nil
	})
}

// walk calls fn for every top-level field of a serialised message. v is the field's payload for
// length-delimited fields and nil otherwise.
func walk(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return malformed("%v", protowire.ParseError(n))
		}
		b = b[n:]
		var v []byte
		if typ == protowire.BytesType {
			v, n = protowire.ConsumeBytes(b)
		} else {
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return malformed("%v", protowire.ParseError(n))
		}
		b = b[n:]
		if err := fn(num, typ, v); err != nil {
			return err
		}
	}
	return nil
}
