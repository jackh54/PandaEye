package dev.pandascript.pandaeye.paper.state;

/**
 * One client movement packet after carry-forward: position/rotation are the client's current values (the
 * previous ones when the packet did not carry them), deltas are relative to the previous packet. The raw
 * fields are exactly what the packet carried (0 for absent parts), for protocol checks.
 *
 * @param tick        tick-end count when the packet arrived (tick clients)
 * @param consecutive this packet is the first movement packet of the tick directly after the previous one's
 * @param teleport    the PosRot that answers a confirmed teleport (new baseline; its onGround is meaningless)
 * @param flags       {@code MoveFlag} bits
 */
public record Move(
        long nanos,
        int tick,
        double x,
        double y,
        double z,
        float yaw,
        float pitch,
        boolean onGround,
        boolean hasPos,
        boolean hasRot,
        boolean horizontalCollision,
        double dx,
        double dy,
        double dz,
        boolean consecutive,
        boolean teleport,
        boolean cancelled,
        int flags,
        double rawX,
        double rawY,
        double rawZ,
        float rawYaw,
        float rawPitch) {

    /** Horizontal displacement. */
    public double h() {
        return Math.sqrt(dx * dx + dz * dz);
    }

    public boolean has(int moveFlag) {
        return (flags & moveFlag) != 0;
    }
}
