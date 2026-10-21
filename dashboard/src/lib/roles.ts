// Roles and permissions (ARCHITECTURE section 7): viewer reads; staff confirm/overturn/decide appeals and
// request watches; admin settings, checks, invites and erasure; owner users and API keys.

export const ROLES = ["viewer", "staff", "admin", "owner"] as const;
export type Role = (typeof ROLES)[number];

export const ROLE_RANK: Record<Role, number> = { viewer: 1, staff: 2, admin: 3, owner: 4 };

export function isRole(x: unknown): x is Role {
  return typeof x === "string" && (ROLES as readonly string[]).includes(x);
}

export function hasRole(actual: Role, minimum: Role): boolean {
  return ROLE_RANK[actual] >= ROLE_RANK[minimum];
}

export const PERMISSIONS = {
  read: "viewer",
  "verdicts:decide": "staff",
  "appeals:decide": "staff",
  "players:watch": "staff",
  "settings:write": "admin",
  "checks:write": "admin",
  "invites:manage": "admin",
  "users:view": "admin",
  "players:erase": "admin",
  "audit:read": "admin",
  "users:manage": "owner",
  "apikeys:manage": "owner",
} as const satisfies Record<string, Role>;

export type Permission = keyof typeof PERMISSIONS;

export function can(role: Role, permission: Permission): boolean {
  return hasRole(role, PERMISSIONS[permission]);
}

/** Roles an invite created by `role` may grant: strictly lower than the creator's role. */
export function invitableRoles(role: Role): Role[] {
  if (!can(role, "invites:manage")) return [];
  return ROLES.filter((r) => ROLE_RANK[r] < ROLE_RANK[role]);
}

/** Roles an owner may assign to another user. */
export function assignableRoles(role: Role): Role[] {
  return can(role, "users:manage") ? [...ROLES] : [];
}

export const ROLE_LABEL: Record<Role, string> = {
  viewer: "Viewer",
  staff: "Staff",
  admin: "Admin",
  owner: "Owner",
};
