# Codec fixtures

Cross-language fixtures for the wire frame codec (ARCHITECTURE section 3, `proto/pandaeye/v1/wire.proto`).
They are produced by the Java plugin code and must decode identically in the Go gateway (`internal/codec`).

Every fixture `<name>` has:

| File | Content |
|---|---|
| `<name>.pb` | the serialized `pandaeye.v1.ClientFrame` (raw protobuf, no codec byte) |
| `<name>.raw.bin` | WebSocket message with codec byte `0x00` followed by the `.pb` bytes |
| `<name>.zstd.bin` | codec byte `0x01` followed by one zstd frame (content size set) of the `.pb` bytes, written by aircompressor 0.27 |
| `<name>.deflate.bin` | codec byte `0x02` followed by a raw DEFLATE stream (RFC 1951, `Deflater(nowrap=true)`) of the `.pb` bytes |

A decoder passes when each `.bin` file decodes to exactly the bytes of `<name>.pb` and `<name>.pb` parses
as a `ClientFrame`. The fixtures exercise every codec regardless of the 1 KiB compression threshold the
plugin applies when sending (Hello is only ever sent as `0x00` on the wire).

`manifest.json` lists each fixture with its frame kind, `.pb` size, SHA-256 of the `.pb` bytes, and for
batches the seq and event count:

| Name | Content |
|---|---|
| `hello` | Hello: protocol 1, server id `3f1c2b4a-0000-4000-8000-000000000001`, platform FOLIA, shadow mode, 4 capabilities, 3 online players |
| `batch_small` | Batch seq 1: a PlayerJoin (`Fixture_1`, protocol 774, entity id 42) and one MoveSample |
| `batch_moves` | Batch seq 2, `replayed = true`: 200 MoveSamples, event i has `tick = i + 1`, `x = i * 0.25` |
| `batch_flag` | Batch seq 3: one LocalFlag `movement.speed` with `data = {excess: 0.15, bound: 0.31}` and 40 context moves |

The player of every event is the 16 bytes `0123456789abcdef0123456789abcdef` (hex).

Regenerate with `gradle -p plugin :common:writeFixtures`; `CodecFixturesTest` fails when the committed
files drift from the generator.
