package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackh54/PandaEye/cloud/internal/codec"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// handleClip decodes a stored clip (zstd ClipData) into the replay viewer's JSON:
// {player, player_name?, start_us, end_us, events: [{player, time_us, tick, <oneof field>: {...}}]},
// events in protojson with proto field names; UUID bytes (player, target_player) as canonical UUIDs.
func (s *Service) handleClip(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	id, err := pathID(r, "id")
	if err != nil {
		return notFound("clip")
	}
	c, err := s.d.Store.GetClip(r.Context(), rc.tenant(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("clip")
		}
		return err
	}
	if c.Codec != store.ClipCodecZstd {
		return errf(http.StatusInternalServerError, codeInternal, "unsupported clip codec")
	}
	raw, err := s.clips.DecodeZstd(r.Context(), c.Data, codec.MaxDecompressed)
	if err != nil {
		return err
	}
	var cd pb.ClipData
	if err := proto.Unmarshal(raw, &cd); err != nil {
		return err
	}
	out, err := clipJSON(c, &cd, s.lookups(r.Context(), rc.tenant()).playerName(c.PlayerUUID))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}

func clipJSON(c *store.Clip, cd *pb.ClipData, playerName string) ([]byte, error) {
	player := c.PlayerUUID
	if u, err := identity.UUIDFromBytes(cd.Player); err == nil && !u.IsZero() {
		player = u.String()
	}
	start, end := c.StartUs, c.EndUs
	if cd.StartUs != 0 {
		start = int64(cd.StartUs)
	}
	if cd.EndUs != 0 {
		end = int64(cd.EndUs)
	}
	mo := protojson.MarshalOptions{UseProtoNames: true}
	events := make([]any, 0, len(cd.Events))
	for _, e := range cd.Events {
		b, err := mo.Marshal(e)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		uuidFields(m)
		if _, ok := m["player"]; !ok {
			m["player"] = ""
		}
		if _, ok := m["time_us"]; !ok {
			m["time_us"] = "0"
		}
		if _, ok := m["tick"]; !ok {
			m["tick"] = 0
		}
		events = append(events, m)
	}
	doc := map[string]any{"player": player, "start_us": start, "end_us": end, "events": events}
	if playerName != "" {
		doc["player_name"] = playerName
	}
	return json.Marshal(doc)
}

// uuidFields rewrites the UUID byte fields (base64 in protojson) of v and its children as canonical
// UUID strings.
func uuidFields(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && (k == "player" || k == "target_player") {
				if b, err := base64.StdEncoding.DecodeString(s); err == nil {
					if u, err := identity.UUIDFromBytes(b); err == nil {
						t[k] = u.String()
					}
				}
				continue
			}
			uuidFields(x)
		}
	case []any:
		for _, x := range t {
			uuidFields(x)
		}
	}
}
