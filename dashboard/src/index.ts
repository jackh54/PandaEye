export default { async fetch(req: Request, env: any) { const r = await env.DB.prepare("SELECT 1 AS x").first(); return new Response(JSON.stringify(r)); } };
