package dev.pandascript.pandaeye.paper.env;

import org.bukkit.entity.Entity;
import org.bukkit.entity.LivingEntity;
import org.bukkit.entity.Vehicle;

/** Entities that push or collide with players (players, living entities, boats, minecarts, ...). */
public final class Collidables {

    private Collidables() {
    }

    public static boolean pushesOrCollides(Entity entity) {
        if (entity instanceof LivingEntity living) {
            return living.isCollidable();
        }
        return entity instanceof Vehicle;
    }
}
