package testkit

import (
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/geom"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// NewServerID returns a random canonical server id.
func NewServerID() string { return identity.NewUUID().String() }

// NewPlayerID returns a random player UUID in wire form.
func NewPlayerID() []byte { return identity.NewUUID().Bytes() }

// Hello returns a Hello as a Paper 1.21 plugin in shadow mode sends it on its first connect.
func Hello(licenseKey, serverID string) *pb.Hello {
	return &pb.Hello{
		ProtocolVersion: 1, LicenseKey: licenseKey, ServerId: serverID, ServerName: "test-server",
		Platform: pb.Platform_PLATFORM_PAPER, PlatformVersion: "1.21.11-R0.1", PluginVersion: "0.1.0",
		MinecraftVersion: "1.21.11", ShadowMode: true, ClientTimeUnixMs: uint64(time.Now().UnixMilli()),
		Capabilities: []string{"tick_end", "env_sampling", "target_history", "probe"},
		InstanceId:   identity.NewUUID().String(),
	}
}

// Batch wraps events in a Batch sent now.
func Batch(seq uint64, events ...*pb.Event) *pb.Batch {
	return &pb.Batch{Seq: seq, SentUnixMs: uint64(time.Now().UnixMilli()), Events: events}
}

// Probe returns a server-level probe event.
func Probe(probeID uint64, timeUs uint64) *pb.Event {
	return &pb.Event{TimeUs: timeUs, Kind: &pb.Event_Probe{Probe: &pb.Probe{ProbeId: probeID}}}
}

// Stall returns a server-level ServerStall event.
func Stall(startUs, endUs uint64, share float32) *pb.Event {
	return &pb.Event{TimeUs: endUs, Kind: &pb.Event_Stall{Stall: &pb.ServerStall{StartUs: startUs, EndUs: endUs, StalledShare: share}}}
}

// Player simulates one tick-client player: every Move advances the client tick and the event clock
// by 50 ms. The zero value is not usable; use NewPlayer.
type Player struct {
	UUID     []byte
	Name     string
	Session  string
	Protocol int32
	Pos      geom.Vec3
	Rot      geom.Rotation
	OnGround bool
	Tick     uint32
	TimeUs   uint64 // event clock of the next event
	EntityID int32
}

// NewPlayer returns a standing player at (0, 64, 0) with a new session, clock starting now.
func NewPlayer(name string) *Player {
	return &Player{UUID: NewPlayerID(), Name: name, Session: identity.NewUUID().String(), Protocol: 769,
		Pos: geom.V(0, 64, 0), OnGround: true, TimeUs: uint64(time.Now().UnixMicro()), EntityID: 1000}
}

func (p *Player) event() *pb.Event { return &pb.Event{Player: p.UUID, TimeUs: p.TimeUs, Tick: p.Tick} }

// Advance moves the event clock forward by d without sending anything.
func (p *Player) Advance(d time.Duration) { p.TimeUs += uint64(d.Microseconds()) }

// Join returns the PlayerJoin of a new session.
func (p *Player) Join() *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_Join{Join: &pb.PlayerJoin{Name: p.Name, ProtocolVersion: p.Protocol, ClientVersion: "1.21.4",
		Brand: "vanilla", EntityId: p.EntityID, Session: p.Session}}
	return e
}

// State returns a StateChange with vanilla attributes.
func (p *Player) State() *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_State{State: &pb.StateChange{MovementSpeed: 0.1, JumpStrength: 0.42, Gravity: 0.08, StepHeight: 0.6,
		Scale: 1, EntityReach: 3, BlockReach: 4.5}}
	return e
}

// Quit returns the PlayerQuit of the session.
func (p *Player) Quit() *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_Quit{Quit: &pb.PlayerQuit{Reason: "disconnected"}}
	return e
}

// Move moves the player by d with rotation r in one client tick and returns the MoveSample.
func (p *Player) Move(d geom.Vec3, r geom.Rotation) *pb.Event {
	p.Tick++
	p.TimeUs += 50_000
	p.Pos, p.Rot = p.Pos.Add(d), r
	e := p.event()
	e.Kind = &pb.Event_Move{Move: &pb.MoveSample{X: p.Pos.X, Y: p.Pos.Y, Z: p.Pos.Z, Yaw: float32(r.Yaw), Pitch: float32(r.Pitch),
		OnGround: p.OnGround, HasPos: true, HasRot: true, TicksSinceLast: 1, PacketsSinceLast: 1, Focus: true}}
	return e
}

// Eye returns the player's standing eye position.
func (p *Player) Eye() geom.Vec3 { return geom.Eye(p.Pos, geom.PoseStanding, p.Protocol, 1) }

// Attack returns an attack on target with the attacker's current rotation; history are the target's
// recent positions (oldest first), defaulting to its current position.
func (p *Player) Attack(target *Player, history ...*pb.TimedPos) *pb.Event {
	if len(history) == 0 {
		history = []*pb.TimedPos{{TimeUs: target.TimeUs, X: target.Pos.X, Y: target.Pos.Y, Z: target.Pos.Z}}
	}
	eye := p.Eye()
	e := p.event()
	e.Kind = &pb.Event_Attack{Attack: &pb.Attack{TargetEntityId: target.EntityID, TargetPlayer: target.UUID, TargetType: "minecraft:player",
		Eye: &pb.Vec3{X: eye.X, Y: eye.Y, Z: eye.Z}, Yaw: float32(p.Rot.Yaw), Pitch: float32(p.Rot.Pitch),
		TargetWidth: geom.PlayerWidth, TargetHeight: geom.PlayerHeight, TargetHistory: history, PingMs: 50, EntityReach: 3,
		EyeHeight: float32(eye.Y - p.Pos.Y), RttHighMs: 60}}
	return e
}

// Swing returns a main-hand swing in the current tick.
func (p *Player) Swing() *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_Swing{Swing: &pb.Swing{Hand: pb.Hand_HAND_MAIN}}
	return e
}

// Flag returns a forwarded local flag.
func (p *Player) Flag(checkID string, category pb.Category, severity float64) *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_Flag{Flag: &pb.LocalFlag{CheckId: checkID, Category: category, Vl: 1, Severity: severity, Confidence: 0.8, Confirmed: true}}
	return e
}

// Probe returns a probe event about this player.
func (p *Player) Probe(probeID uint64) *pb.Event {
	e := p.event()
	e.Kind = &pb.Event_Probe{Probe: &pb.Probe{ProbeId: probeID}}
	return e
}
