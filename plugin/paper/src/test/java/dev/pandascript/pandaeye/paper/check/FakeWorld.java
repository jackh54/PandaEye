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
}
