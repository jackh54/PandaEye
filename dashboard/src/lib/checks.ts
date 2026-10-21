// Check catalog mirrored from docs/CHECKS.md section 4 (display only; the cloud catalog is authoritative).
// test/checks.test.ts asserts this table matches the document.

export const CATEGORIES = [
  "AUTO_CLICKER",
  "HEURISTICS",
  "INVALID_PROTOCOL",
  "MOVEMENT",
  "TICK_SPEED",
  "INVENTORY",
  "WORLD_INTERACTION",
  "LATENCY_ABUSE",
] as const;
export type Category = (typeof CATEGORIES)[number];

export const CATEGORY_LABEL: Record<Category, string> = {
  AUTO_CLICKER: "Auto clicker",
  HEURISTICS: "Combat heuristics",
  INVALID_PROTOCOL: "Invalid protocol",
  MOVEMENT: "Movement",
  TICK_SPEED: "Tick speed",
  INVENTORY: "Inventory",
  WORLD_INTERACTION: "World interaction",
  LATENCY_ABUSE: "Latency abuse",
};

export interface CheckInfo {
  id: string;
  where: "local" | "cloud";
  category: Category;
  points: string;
  confidence: string;
  halfLifeS: number;
  cap: number;
  certain: boolean;
  /** Shipped disabled by default ("(off)" in the table). */
  defaultOff: boolean;
}

function c(
  id: string,
  where: "local" | "cloud",
  category: Category,
  points: string,
  confidence: string,
  halfLifeS: number,
  cap: number,
  certain = false,
  defaultOff = false,
): CheckInfo {
  return { id, where, category, points, confidence, halfLifeS, cap, certain, defaultOff };
}

export const CHECKS: readonly CheckInfo[] = [
  c("timer.balance", "local", "TICK_SPEED", "6", "0.85", 300, 60),
  c("movement.speed", "local", "MOVEMENT", "2..6", "0.75", 120, 50),
  c("movement.fly", "local", "MOVEMENT", "4..10", "0.8", 120, 60),
  c("movement.step", "local", "MOVEMENT", "4..8", "0.8", 120, 50),
  c("movement.groundspoof", "local", "MOVEMENT", "3..6", "0.8", 300, 40),
  c("movement.noslow", "local", "MOVEMENT", "3..6", "0.75", 120, 40),
  c("dig.speed", "local", "WORLD_INTERACTION", "3..7", "0.8", 300, 50),
  c("protocol.pitch", "local", "INVALID_PROTOCOL", "25", "1.0", 3600, 200, true),
  c("protocol.position", "local", "INVALID_PROTOCOL", "25", "1.0", 3600, 200, true),
  c("protocol.slot", "local", "INVALID_PROTOCOL", "10 (0 redundant)", "0.95", 600, 20),
  c("protocol.selfhit", "local", "INVALID_PROTOCOL", "25", "1.0", 3600, 200, true),
  c("protocol.cursor", "local", "INVALID_PROTOCOL", "3", "0.6", 600, 15),
  c("protocol.flood", "local", "INVALID_PROTOCOL", "3", "0.7", 600, 20),
  c("protocol.abilities", "local", "INVALID_PROTOCOL", "5", "0.9", 600, 30),
  c("inventory.closed", "local", "INVENTORY", "3", "0.8", 300, 20),
  c("reach.distance", "cloud", "HEURISTICS", "3..13", "0.7..0.95", 300, 80),
  c("killaura.hitbox", "cloud", "HEURISTICS", "5", "0.85", 300, 80),
  c("killaura.multi", "cloud", "HEURISTICS", "10", "0.95", 900, 120, true),
  c("killaura.noswing", "cloud", "HEURISTICS", "6", "0.9", 600, 80),
  c("aim.gcd", "cloud", "HEURISTICS", "4", "0.75", 300, 30),
  c("aim.snap", "cloud", "HEURISTICS", "5", "0.8", 300, 50),
  c("aim.precision", "cloud", "HEURISTICS", "4", "0.7", 300, 30),
  c("velocity.vertical", "cloud", "HEURISTICS", "6", "0.85", 300, 60),
  c("velocity.horizontal", "cloud", "HEURISTICS", "5", "0.8", 300, 50),
  c("autoclicker.consistency", "cloud", "AUTO_CLICKER", "4", "0.7", 300, 30),
  c("autoclicker.pattern", "cloud", "AUTO_CLICKER", "4", "0.7", 300, 30),
  c("autoclicker.cps", "cloud", "AUTO_CLICKER", "2..8", "0.6", 300, 30, false, true),
  c("scaffold.face", "cloud", "WORLD_INTERACTION", "8", "0.95", 600, 80),
  c("scaffold.ray", "cloud", "WORLD_INTERACTION", "6", "0.85", 300, 80),
  c("scaffold.cursor", "cloud", "WORLD_INTERACTION", "5", "0.8", 300, 60),
  c("scaffold.reach", "cloud", "WORLD_INTERACTION", "3..8", "0.8", 300, 50),
  c("dig.ray", "cloud", "WORLD_INTERACTION", "5", "0.85", 300, 60),
  c("inventory.move", "cloud", "INVENTORY", "4", "0.75", 300, 30),
  c("inventory.rotate", "cloud", "INVENTORY", "3", "0.7", 300, 30),
  c("latency.pingspoof", "cloud", "LATENCY_ABUSE", "3", "0.8", 600, 18),
  c("latency.blink", "cloud", "LATENCY_ABUSE", "3", "0.6", 300, 18),
];

export const FAMILY_CAPS: Readonly<Record<string, number>> = {
  timer: 60,
  movement: 80,
  protocol: 200,
  inventory: 40,
  reach: 80,
  killaura: 120,
  aim: 45,
  velocity: 60,
  autoclicker: 40,
  scaffold: 100,
  dig: 70,
  latency: 25,
  ml: 50,
};

const BY_ID = new Map(CHECKS.map((x) => [x.id, x]));

export function checkInfo(id: string): CheckInfo | undefined {
  return BY_ID.get(id);
}

export function familyOf(id: string): string {
  const i = id.indexOf(".");
  return i < 0 ? id : id.slice(0, i);
}

export function cloudChecks(): CheckInfo[] {
  return CHECKS.filter((x) => x.where === "cloud");
}

export function localChecks(): CheckInfo[] {
  return CHECKS.filter((x) => x.where === "local");
}

/** Accepts "HEURISTICS", "CATEGORY_HEURISTICS", "heuristics" or the proto enum number. */
export function normalizeCategory(v: unknown): Category | null {
  if (typeof v === "number") return CATEGORIES[v - 1] ?? null;
  if (typeof v !== "string") return null;
  const s = v.trim().toUpperCase().replace(/^CATEGORY_/, "");
  return (CATEGORIES as readonly string[]).includes(s) ? (s as Category) : null;
}

export function categoryLabel(v: unknown): string {
  const cat = normalizeCategory(v);
  return cat ? CATEGORY_LABEL[cat] : typeof v === "string" && v ? v : "Unknown";
}
