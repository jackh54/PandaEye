import { createApp } from "./app";
import type { Env } from "./env";
import { cleanup } from "./lib/users";

const app = createApp();

export default {
  fetch: app.fetch,
  async scheduled(_controller, env, ctx) {
    ctx.waitUntil(cleanup(env.DB, Date.now()));
  },
} satisfies ExportedHandler<Env>;
