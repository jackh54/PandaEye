package dev.pandascript.pandaeye.common.litebans;

import litebans.api.Database;
import litebans.api.Entry;
import litebans.api.Events;

import java.sql.PreparedStatement;
import java.sql.SQLException;

/**
 * The only class built on LiteBans' API ({@code litebans.api}, compiled against {@code plugin/litebans-stub}). It is
 * loaded only after {@link LiteBansLabels} found LiteBans, and the Paper class preloader skips it, so a server
 * without LiteBans never tries to load it. Forwards bans to {@link LiteBansLabels}, which filters and classifies.
 */
final class LiteBansHook extends Events.Listener implements LiteBansLabels.Api {

    private volatile LiteBansLabels labels;
    private volatile Events events;

    @Override
    public boolean register(LiteBansLabels target) {
        Events api = Events.get();
        if (api == null) {
            return false;
        }
        labels = target;
        api.register(this);
        events = api;
        return true;
    }

    @Override
    public void unregister() {
        Events api = events;
        events = null;
        labels = null;
        if (api != null) {
            api.unregister(this);
        }
    }

    @Override
    public PreparedStatement prepare(String sql) throws SQLException {
        Database database = Database.get();
        if (database == null) {
            throw new SQLException("LiteBans' database is not connected");
        }
        return database.prepareStatement(sql);
    }

    /** Never throws into LiteBans. */
    @Override
    public void entryAdded(Entry entry) {
        LiteBansLabels target = labels;
        if (target == null || entry == null) {
            return;
        }
        try {
            target.added(view(entry));
        } catch (Throwable t) {
            target.failed(t);
        }
    }

    /** Never throws into LiteBans. */
    @Override
    public void entryRemoved(Entry entry) {
        LiteBansLabels target = labels;
        if (target == null || entry == null) {
            return;
        }
        try {
            target.removed(view(entry));
        } catch (Throwable t) {
            target.failed(t);
        }
    }

    static BanEntry view(Entry entry) {
        return new BanEntry(entry.getId(), entry.getType(), entry.getUuid(), entry.getReason(),
                entry.getExecutorUUID(), entry.getRemovalReason(), entry.getDateStart(), entry.getDateEnd());
    }
}
