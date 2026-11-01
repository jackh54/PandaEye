package litebans.api;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;

/** Plays LiteBans in tests: installs (or removes) the stub API's instances and records listeners. */
public final class TestLiteBans {

    private TestLiteBans() {
    }

    public static void install(Events events, Database database) {
        Events.instance = events;
        Database.instance = database;
    }

    public static void uninstall() {
        install(null, null);
    }

    /** An {@link Events} that keeps its listeners, like LiteBans' own. */
    public static final class RecordingEvents extends Events {

        public final List<Listener> listeners = new CopyOnWriteArrayList<>();

        @Override
        public void register(Listener listener) {
            listeners.add(listener);
        }

        @Override
        public void unregister(Listener listener) {
            listeners.remove(listener);
        }
    }
}
