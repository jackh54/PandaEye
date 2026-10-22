package dev.pandascript.pandaeye.paper.check;

import java.util.function.Predicate;

/** Test {@link WorldView}: uniform friction unless overridden, configurable collisions and exempt areas. */
public final class FakeWorld implements WorldView {

    public double maxFriction = 0.6;
    public double minFriction = 0.6;
    public boolean soulSpeed;
    public Predicate<Box> collisions = box -> false;
    public Predicate<Box> exemptNear = box -> false;
    public boolean entityNear;
    /** {@link WorldView#usingItemSpeedMultiplier()}: vanilla's 0.2 unless a test changes it (NaN = not using). */
    public double usingItemSpeedMultiplier = 0.2;
    /** {@link WorldView#blockPresent}: true (the server refused the break) unless a test changes it. */
    public boolean blockPresent = true;

    @Override
    public double maxFriction(double x, double y, double z) {
        return maxFriction;
    }

    @Override
    public double minFriction(double x, double y, double z) {
        return minFriction;
    }

    @Override
    public boolean soulSpeedBlockUnder(double x, double y, double z) {
        return soulSpeed;
    }

    @Override
    public boolean hasCollisions(Box box) {
        return collisions.test(box);
    }

    @Override
    public boolean exemptNear(Box box) {
        return exemptNear.test(box);
    }

    @Override
    public boolean collidableEntityNear(Box box, double radius) {
        return entityNear;
    }

    @Override
    public double usingItemSpeedMultiplier() {
        return usingItemSpeedMultiplier;
    }

    @Override
    public boolean blockPresent(int x, int y, int z) {
        return blockPresent;
    }
}
