import type { Context } from "hono";
import type { Env } from "./env";
import type { FetchFn } from "./lib/api";
import type { PandaEyeApi } from "./lib/pandaeye";
import type { Session } from "./lib/session";

export interface Deps {
  /** Outbound fetch for the Go API and Turnstile (tests inject a mock). */
  fetch: FetchFn;
  now: () => number;
}

export interface Vars {
  nonce: string;
  /** Set by pages that embed Turnstile; widens the CSP for that response only. */
  turnstile: boolean;
  clientIp: string | null;
  session: Session | null;
  api: PandaEyeApi;
  deps: Deps;
}

export type AppEnv = { Bindings: Env; Variables: Vars };
export type Ctx = Context<AppEnv>;
