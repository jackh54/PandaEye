package litebans.api;

import java.sql.PreparedStatement;
import java.sql.SQLException;

/** Compile-only stub of LiteBans' {@code litebans.api.Database} (see {@link Events}). */
public abstract class Database {

    /** Set by LiteBans once its database is connected (tests: {@code litebans.api.TestLiteBans}). */
    static Database instance;

    public static Database get() {
        return instance;
    }

    /**
     * A statement on LiteBans' own connection; {@code {bans}} in the SQL becomes its bans table. The caller closes
     * the statement, never the connection.
     */
    public abstract PreparedStatement prepareStatement(String sql) throws SQLException;
}
