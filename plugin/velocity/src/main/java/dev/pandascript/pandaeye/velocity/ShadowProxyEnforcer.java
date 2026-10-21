package dev.pandascript.pandaeye.velocity;

import com.velocitypowered.api.proxy.Player;
import net.kyori.adventure.text.Component;

import java.util.concurrent.atomic.LongAdder;

/** Shadow mode: does nothing, counts what would have been done. */
final class ShadowProxyEnforcer implements ProxyEnforcer {

    private final LongAdder calls = new LongAdder();

    @Override
    public boolean live() {
        return false;
    }

    @Override
    public void disconnect(Player player, Component message) {
        calls.increment();
    }

    @Override
    public void command(String command) {
        calls.increment();
    }

    @Override
    public long calls() {
        return calls.sum();
    }
}
