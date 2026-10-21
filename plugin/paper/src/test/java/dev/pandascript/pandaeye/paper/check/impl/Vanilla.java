package dev.pandascript.pandaeye.paper.check.impl;

import dev.pandascript.pandaeye.paper.check.CheckHarness;

/** Vanilla 1.21 player motion for check tests: one {@link CheckHarness#step} per client tick. */
final class Vanilla {

    static final double GRAVITY = 0.08;
    static final double DRAG = 0.98;
    static final double JUMP = 0.42;

    private Vanilla() {
    }

    /** Vertical motion after one airborne tick (gravity, drag, small motion zeroed). */
    static double fall(double motion, double gravity) {
        double next = (motion - gravity) * DRAG;
        return Math.abs(next) < 0.003 ? 0 : next;
    }

    /**
     * Jumps with {@code power} from the current ground and lands on the same level. {@code headroom} is the
     * gap between the head and a ceiling (hitting it zeroes the vertical motion), {@code h} the horizontal
     * displacement per tick along x. Returns the number of ticks.
     */
    static int jump(CheckHarness harness, double power, double headroom, double h) {
        double y = 0;
        double v = power;
        int ticks = 0;
        while (true) {
            double move = v;
            if (y + move > headroom) {
                move = headroom - y;
                v = 0;
            }
            boolean land = y + move <= 0;
            if (land) {
                move = -y;
            }
            y += move;
            harness.step(h, move, 0, land);
            ticks++;
            if (land) {
                return ticks;
            }
            v = fall(v, GRAVITY);
        }
    }

    /** Walks off a ledge and falls for {@code ticks} ticks (motion starts at the on-ground -0.0784). */
    static double fallFor(CheckHarness harness, int ticks, double gravity) {
        double v = fall(0, gravity);
        for (int i = 0; i < ticks; i++) {
            harness.step(0.1, v, 0, false);
            v = fall(v, gravity);
        }
        return v;
    }
}
