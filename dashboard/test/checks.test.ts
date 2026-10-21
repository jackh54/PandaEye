import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { CHECKS, FAMILY_CAPS, normalizeCategory } from "../src/lib/checks";

/** Parses docs/CHECKS.md section 4 (provided by vitest.config.ts). */
function parseTable(md: string) {
  const section = md.slice(md.indexOf("## 4. Scoring summary"));
  const rows: { id: string; where: string; category: string; points: string; conf: string; half: number; cap: number; certain: boolean; off: boolean }[] = [];
  for (const line of section.split("\n")) {
    const cells = line.split("|").map((c) => c.trim());
    if (cells.length < 9 || !/^[a-z]+\.[a-z_]+/.test(cells[1]!)) continue;
    const off = /\(off\)/.test(cells[1]!);
    rows.push({
      id: cells[1]!.replace(/\s*\(off\)/, ""),
      where: cells[2]!,
      category: cells[3]!,
      points: cells[4]!,
      conf: cells[5]!,
      half: Number(cells[6]),
      cap: Number(cells[7]),
      certain: cells[8] === "yes",
      off,
    });
  }
  const capsLine = section.slice(section.indexOf("Family caps")).split("`Signal.points`")[0]!;
  const caps: Record<string, number> = {};
  for (const m of capsLine.matchAll(/([a-z]+) (\d+)/g)) caps[m[1]!] = Number(m[2]);
  return { rows, caps };
}

describe("check catalog matches docs/CHECKS.md section 4", () => {
  const { rows, caps } = parseTable(env.TEST_CHECKS_MD);

  it("has every check of the table with the same attributes", () => {
    expect(rows.length).toBeGreaterThan(30);
    expect(CHECKS.map((c) => c.id)).toEqual(rows.map((r) => r.id));
    for (const r of rows) {
      const c = CHECKS.find((x) => x.id === r.id)!;
      expect({ id: c.id, where: c.where, category: c.category, points: c.points, conf: c.confidence, half: c.halfLifeS, cap: c.cap, certain: c.certain, off: c.defaultOff }).toEqual(
        { id: r.id, where: r.where, category: r.category, points: r.points, conf: r.conf, half: r.half, cap: r.cap, certain: r.certain, off: r.off },
      );
    }
  });

  it("has the family caps", () => {
    expect(FAMILY_CAPS).toEqual(caps);
  });

  it("normalises category spellings", () => {
    expect(normalizeCategory("CATEGORY_WORLD_INTERACTION")).toBe("WORLD_INTERACTION");
    expect(normalizeCategory("heuristics")).toBe("HEURISTICS");
    expect(normalizeCategory(1)).toBe("AUTO_CLICKER");
    expect(normalizeCategory("nope")).toBeNull();
  });
});
