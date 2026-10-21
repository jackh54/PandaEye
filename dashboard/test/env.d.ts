import type { D1Migration } from "cloudflare:test";
import type { Env as DashboardEnv } from "../src/env";

declare global {
  namespace Cloudflare {
    interface Env extends DashboardEnv {
      TEST_MIGRATIONS: D1Migration[];
      TEST_CHECKS_MD: string;
      TEST_SOURCES: string;
    }
  }
}
