import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { cloudflareTest, readD1Migrations } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

// Tests run inside workerd (the Workers runtime) with a real local D1 database and the assets binding
// from wrangler.toml. Outbound calls to the Go API and Turnstile go through an injected mock fetch.
export default defineConfig(async () => {
  const migrations = await readD1Migrations("./migrations");
  const checksMd = readFileSync(new URL("../docs/CHECKS.md", import.meta.url), "utf8");
  // Source texts for static security assertions (no raw HTML APIs anywhere).
  const sources: Record<string, string> = {};
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (/\.(tsx?|js)$/.test(e.name)) sources[p] = readFileSync(p, "utf8");
    }
  };
  walk("src");
  walk("public/static");
  return {
    plugins: [
      cloudflareTest({
        wrangler: { configPath: "./wrangler.toml" },
        miniflare: {
          bindings: {
            TEST_MIGRATIONS: migrations,
            TEST_CHECKS_MD: checksMd,
            TEST_SOURCES: JSON.stringify(sources),
            ENVIRONMENT: "production",
            PANDAEYE_API_URL: "https://api.pandaeye.test",
            PANDAEYE_SERVICE_TOKEN: "test-service-token-0123456789abcdef0123",
            PANDAEYE_ASSERTION_KEY: "test-assertion-key-0123456789abcdef0123",
            PASSWORD_PEPPER: "test-password-pepper-0123456789abcdef01",
            TURNSTILE_SECRET: "test-turnstile-secret",
            TURNSTILE_SITEKEY: "test-sitekey",
          },
        },
      }),
    ],
    test: {
      setupFiles: ["./test/setup.ts"],
      testTimeout: 30_000,
    },
  };
});
