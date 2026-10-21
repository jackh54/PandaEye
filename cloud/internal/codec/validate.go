package codec

import (
	"strings"
	"unicode/utf8"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// validateClientFrame applies the post-decode field limits (ARCHITECTURE 5.3): strings are truncated
// to 256 bytes (info to 512) at a rune boundary, repeated fields are bounded, and UUID byte fields
// must be empty or 16 bytes.
func validateClientFrame(f *pb.ClientFrame) error {
	switch k := f.Kind.(type) {
	case *pb.ClientFrame_Hello:
		return validateHello(k.Hello)
	case *pb.ClientFrame_Batch:
		for _, e := range k.Batch.GetEvents() {
			if err := validateEvent(e); err != nil {
				return err
			}
		}
	case *pb.ClientFrame_VerdictAck:
		trunc(&k.VerdictAck.VerdictId, MaxStringBytes)
		trunc(&k.VerdictAck.Note, MaxStringBytes)
	case *pb.ClientFrame_Goodbye:
		trunc(&k.Goodbye.Reason, MaxStringBytes)
	}
	return nil
}

func validateHello(h *pb.Hello) error {
	if h == nil {
		return malformed("empty hello")
	}
	if len(h.Capabilities) > MaxCapabilities {
		return tooLarge("more than %d capabilities", MaxCapabilities)
	}
	for i := range h.Capabilities {
		trunc(&h.Capabilities[i], MaxStringBytes)
	}
	for _, s := range []*string{&h.LicenseKey, &h.ServerId, &h.ServerName, &h.NetworkId, &h.PlatformVersion,
		&h.PluginVersion, &h.MinecraftVersion, &h.InstanceId, &h.ServerSecret} {
		trunc(s, MaxStringBytes)
	}
	return nil
}

func validateEvent(e *pb.Event) error {
	if e == nil {
		return malformed("nil event")
	}
	if !uuidBytes(e.Player) {
		return malformed("event player must be empty or 16 bytes")
	}
	switch k := e.Kind.(type) {
	case *pb.Event_Join:
		j := k.Join
		for _, s := range []*string{&j.Name, &j.ClientVersion, &j.Brand, &j.NetworkSession, &j.Session} {
			trunc(s, MaxStringBytes)
		}
	case *pb.Event_Quit:
		trunc(&k.Quit.Reason, MaxStringBytes)
	case *pb.Event_Attack:
		a := k.Attack
		if !uuidBytes(a.TargetPlayer) {
			return malformed("attack target_player must be empty or 16 bytes")
		}
		if len(a.TargetHistory) > MaxTargetHistory {
			return tooLarge("target_history has %d entries", len(a.TargetHistory))
		}
		trunc(&a.TargetType, MaxStringBytes)
	case *pb.Event_Place:
		trunc(&k.Place.Item, MaxStringBytes)
	case *pb.Event_Click:
		trunc(&k.Click.ContainerType, MaxStringBytes)
	case *pb.Event_Flag:
		return validateFlag(k.Flag)
	case *pb.Event_State:
		if len(k.State.Effects) > MaxEffects {
			return tooLarge("effects has %d entries", len(k.State.Effects))
		}
		for _, p := range k.State.Effects {
			trunc(&p.Effect, MaxStringBytes)
		}
	case *pb.Event_UseItem:
		trunc(&k.UseItem.Item, MaxStringBytes)
	case *pb.Event_ServerSwitch:
		s := k.ServerSwitch
		for _, f := range []*string{&s.FromServer, &s.ToServer, &s.NetworkSession} {
			trunc(f, MaxStringBytes)
		}
	case *pb.Event_CheckStats:
		trunc(&k.CheckStats.CheckId, MaxStringBytes)
	}
	return nil
}

func validateFlag(f *pb.LocalFlag) error {
	if len(f.Data) > MaxDataEntries {
		return tooLarge("flag data has %d entries", len(f.Data))
	}
	if len(f.Context) > MaxContextMoves {
		return tooLarge("flag context has %d moves", len(f.Context))
	}
	trunc(&f.CheckId, MaxStringBytes)
	trunc(&f.Variant, MaxStringBytes)
	trunc(&f.Info, MaxInfoBytes)
	for k, v := range f.Data {
		if len(k) > MaxStringBytes {
			delete(f.Data, k)
			t := k
			trunc(&t, MaxStringBytes)
			f.Data[t] = v
		}
	}
	return nil
}

func uuidBytes(b []byte) bool { return len(b) == 0 || len(b) == 16 }

// trunc shortens *s to at most max bytes without splitting a UTF-8 sequence.
func trunc(s *string, max int) {
	// NUL is valid UTF-8 but Postgres text cannot hold it; inserting it fails the whole write.
	if strings.IndexByte(*s, 0) >= 0 {
		*s = strings.ReplaceAll(*s, "\x00", "")
	}
	if len(*s) <= max {
		return
	}
	cut := max
	for cut > 0 && !utf8.RuneStart((*s)[cut]) {
		cut--
	}
	*s = (*s)[:cut]
}
