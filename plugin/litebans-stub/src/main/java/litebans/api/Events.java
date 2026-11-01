package litebans.api;

/**
 * Compile-only stub of LiteBans' {@code litebans.api.Events}: the members PandaEye uses, with the shapes of the
 * real API (abstract classes). Never shipped; at run time the LiteBans plugin provides the real class.
 */
public abstract class Events {

    /** Set by LiteBans once its API is up (tests: {@code litebans.api.TestLiteBans}). */
    static Events instance;

    public static Events get() {
        return instance;
    }

    public abstract void register(Listener listener);

    public abstract void unregister(Listener listener);

    public abstract static class Listener {

        /** After an entry (ban, mute, warn, kick) was added. */
        public void entryAdded(Entry entry) {
        }

        /** After an entry was removed (unban, unmute, unwarn). */
        public void entryRemoved(Entry entry) {
        }

        public void broadcastSent(String message, String type) {
        }
    }
}
