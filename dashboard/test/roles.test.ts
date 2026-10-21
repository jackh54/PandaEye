import { describe, expect, it } from "vitest";
import { assignableRoles, can, hasRole, invitableRoles, PERMISSIONS, ROLES, type Permission, type Role } from "../src/lib/roles";

describe("role permissions (ARCHITECTURE 7)", () => {
  const expected: Record<Permission, Role[]> = {
    read: ["viewer", "staff", "admin", "owner"],
    "verdicts:decide": ["staff", "admin", "owner"],
    "appeals:decide": ["staff", "admin", "owner"],
    "players:watch": ["staff", "admin", "owner"],
    "settings:write": ["admin", "owner"],
    "checks:write": ["admin", "owner"],
    "invites:manage": ["admin", "owner"],
    "users:view": ["admin", "owner"],
    "players:erase": ["admin", "owner"],
    "audit:read": ["admin", "owner"],
    "users:manage": ["owner"],
    "apikeys:manage": ["owner"],
  };
  for (const perm of Object.keys(PERMISSIONS) as Permission[]) {
    it(`grants ${perm} to ${expected[perm].join(", ")}`, () => {
      expect(ROLES.filter((r) => can(r, perm))).toEqual(expected[perm]);
    });
  }

  it("orders roles", () => {
    expect(hasRole("owner", "admin")).toBe(true);
    expect(hasRole("staff", "admin")).toBe(false);
    expect(hasRole("viewer", "viewer")).toBe(true);
  });

  it("lets invites grant only lower roles", () => {
    expect(invitableRoles("owner")).toEqual(["viewer", "staff", "admin"]);
    expect(invitableRoles("admin")).toEqual(["viewer", "staff"]);
    expect(invitableRoles("staff")).toEqual([]);
    expect(invitableRoles("viewer")).toEqual([]);
    expect(assignableRoles("owner")).toEqual(["viewer", "staff", "admin", "owner"]);
    expect(assignableRoles("admin")).toEqual([]);
  });
});
