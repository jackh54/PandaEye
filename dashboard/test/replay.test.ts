import { describe, expect, it } from "vitest";
import "../public/static/replay.js";

interface ReplayApi {
  normalizeClip(raw: unknown): any;
  uuidOf(v: unknown): string;
  viewDirection(yaw: number, pitch: number): { x: number; y: number; z: number };
  positionAt(list: { t: number; x: number; y: number; z: number }[], t: number): { x: number; y: number; z: number } | null;
}

const R = (globalThis as unknown as { PandaEyeReplay: ReplayApi }).PandaEyeReplay;
const SUSPECT = "8667ba71-b85a-4004-af54-457a9734eed7";
const TARGET = "11111111-2222-3333-4444-555555555555";

function b64uuid(u: string): string {
  const hex = u.replace(/-/g, "");
  let bin = "";
  for (let i = 0; i < 32; i += 2) bin += String.fromCharCode(parseInt(hex.slice(i, i + 2), 16));
  return btoa(bin);
}

describe("replay viewer clip normalisation", () => {
  it("exposes its helpers without a DOM", () => {
    expect(typeof R.normalizeClip).toBe("function");
  });

  it("decodes UUIDs from strings and protojson base64 bytes", () => {
    expect(R.uuidOf(SUSPECT.toUpperCase())).toBe(SUSPECT);
    expect(R.uuidOf(SUSPECT.replace(/-/g, ""))).toBe(SUSPECT);
    expect(R.uuidOf(b64uuid(SUSPECT))).toBe(SUSPECT);
    expect(R.uuidOf(42)).toBe("");
  });

  it("follows Minecraft's yaw/pitch convention for view rays", () => {
    const south = R.viewDirection(0, 0);
    expect(south.z).toBeCloseTo(1);
    expect(south.x).toBeCloseTo(0);
    const west = R.viewDirection(90, 0);
    expect(west.x).toBeCloseTo(-1);
    const down = R.viewDirection(0, 90);
    expect(down.y).toBeCloseTo(-1);
  });

  it("normalises snake_case JSON with every event kind", () => {
    const clip = R.normalizeClip({
      player: SUSPECT,
      player_name: "Suspect",
      events: [
        { player: SUSPECT, time_us: 1_000_000, move: { x: 0, y: 64, z: 0, yaw: 0, pitch: 0, on_ground: true, has_pos: true, has_rot: true, flags: 1 } },
        { player: SUSPECT, time_us: 1_050_000, move: { yaw: 10, pitch: 5, has_pos: false, has_rot: true } },
        { player: SUSPECT, time_us: 1_100_000, move: { x: 0.2, y: 64, z: 0.1, has_pos: true, has_rot: false } },
        {
          player: SUSPECT,
          time_us: 1_200_000,
          attack: {
            target_entity_id: 7,
            target_player: TARGET,
            eye: { x: 0.2, y: 65.62, z: 0.1 },
            yaw: 10,
            pitch: 5,
            target_width: 0.6,
            target_height: 1.8,
            target_history: [
              { time_us: 1_150_000, x: 2, y: 64, z: 1 },
              { time_us: 1_190_000, x: 2.1, y: 64, z: 1 },
            ],
            ping_ms: 40,
          },
        },
        { player: SUSPECT, time_us: 1_210_000, swing: { hand: "HAND_MAIN" } },
        { player: SUSPECT, time_us: 1_300_000, place: { block: { x: 1, y: 63, z: 1 }, face: "FACE_UP", eye: { x: 0, y: 65.6, z: 0 } } },
        { player: SUSPECT, time_us: 1_310_000, dig: { action: "ACTION_FINISH", block: { x: 3, y: 63, z: 3 }, face: 1 } },
        { player: SUSPECT, time_us: 1_400_000, velocity: { source: "SOURCE_EXPLOSION", vx: 0.1, vy: 0.4, vz: 0 } },
        { player: SUSPECT, time_us: 1_500_000, teleport: { teleport_id: 1, x: 5, y: 64, z: 5, confirmed: true } },
        { player: SUSPECT, time_us: 1_600_000, env: { flags: 1 | 1024, x: 5, y: 64, z: 5, server_tick_ms: 50 } },
        {
          player: SUSPECT,
          time_us: 1_700_000,
          flag: { check_id: "movement.speed", info: "too fast", context: [{ time_us: 1_650_000, move: { x: 5.5, y: 64, z: 5, has_pos: true } }] },
        },
        { player: TARGET, time_us: 1_800_000, move: { x: 100, y: 64, z: 100 } },
      ],
    });
    expect(clip.player).toBe(SUSPECT);
    expect(clip.playerName).toBe("Suspect");
    expect(clip.start).toBe(1000);
    expect(clip.end).toBe(1700);
    expect(clip.positions.map((p: { x: number }) => p.x)).toEqual([0, 0.2, 5.5]);
    expect(clip.rotations).toHaveLength(2);
    expect(clip.attacks[0].target).toBe(TARGET);
    expect(clip.targets[TARGET].positions).toHaveLength(2);
    expect(clip.swings).toHaveLength(1);
    expect(clip.places[0].face).toBe("up");
    expect(clip.digs[0].action).toBe("finish");
    expect(clip.digs[0].face).toBe("up");
    expect(clip.velocities[0].explosion).toBe(true);
    expect(clip.teleports[0]).toMatchObject({ x: 5, confirmed: true });
    expect(clip.envs[0].flags).toBe(1025);
    expect(clip.flags[0].check).toBe("movement.speed");
  });

  it("accepts protojson camelCase with string 64-bit numbers and base64 UUIDs", () => {
    const clip = R.normalizeClip({
      origin: { serverId: "s1" },
      player: b64uuid(SUSPECT),
      events: [
        { player: b64uuid(SUSPECT), timeUs: "2000000", move: { x: 1, y: 70, z: 2, hasPos: true, hasRot: true, yaw: 90, pitch: 0, onGround: true } },
        { player: b64uuid(SUSPECT), timeUs: "2100000", attack: { targetEntityId: 3, eye: { x: 1, y: 71.62, z: 2 }, targetHistory: [{ timeUs: "2090000", x: 3, y: 70, z: 2 }] } },
        { player: b64uuid(TARGET), timeUs: "2200000", move: { x: 9, y: 9, z: 9 } },
      ],
    });
    expect(clip.player).toBe(SUSPECT);
    expect(clip.positions).toHaveLength(1);
    expect(clip.attacks[0].target).toBe("entity 3");
    expect(clip.targets["entity 3"].positions[0]).toMatchObject({ t: 2090, x: 3 });
  });

  it("ignores garbage without throwing", () => {
    for (const bad of [null, 42, "x", [], { events: "nope" }, { events: [null, 1, "x", { time_us: "abc", move: {} }, { time_us: 1, move: { x: "NaN" } }] }]) {
      const c = R.normalizeClip(bad);
      expect(Array.isArray(c.moves)).toBe(true);
      expect(c.positions).toHaveLength(0);
    }
  });

  it("interpolates positions between samples", () => {
    const list = [
      { t: 0, x: 0, y: 0, z: 0 },
      { t: 100, x: 10, y: 0, z: 0 },
    ];
    expect(R.positionAt(list, 50)!.x).toBeCloseTo(5);
    expect(R.positionAt(list, 150)!.x).toBe(10);
  });
});
