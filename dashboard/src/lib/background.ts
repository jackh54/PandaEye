import type { Ctx } from "../types";

/**
 * Runs `work` after the response (ExecutionContext.waitUntil), so optional writes such as audit rows never
 * change a response's timing. Failures are logged, never surfaced.
 */
export function inBackground(c: Ctx, work: Promise<unknown>): void {
  const safe = work.catch((e: unknown) => console.error("pandaeye-dashboard: background task failed", e instanceof Error ? e.message : String(e)));
  try {
    c.executionCtx.waitUntil(safe);
  } catch {
    // No ExecutionContext (not the case on Workers): the promise still runs to completion on its own.
  }
}
