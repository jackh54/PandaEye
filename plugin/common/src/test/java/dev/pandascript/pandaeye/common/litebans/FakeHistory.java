package dev.pandascript.pandaeye.common.litebans;

import java.lang.reflect.Proxy;
import java.sql.PreparedStatement;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Timestamp;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.IntConsumer;

/**
 * A LiteBans bans table for the backfill: a {@link PreparedStatement} over fixed rows that records its parameters,
 * its max rows, the thread of the query and whether it and its result set were closed.
 */
final class FakeHistory {

    final List<Map<String, Object>> rows;
    final Map<Integer, Object> params = new HashMap<>();
    int maxRows;
    boolean statementClosed;
    boolean resultClosed;
    Thread queryThread;
    /** Runs after each row was handed out (index of the row). */
    IntConsumer afterRow = row -> { };

    FakeHistory(List<Map<String, Object>> rows) {
        this.rows = rows;
    }

    /** One bans-table row; {@code removedAt} may be a {@link Timestamp}, null or an {@link SQLException} to throw. */
    static Map<String, Object> row(long id, String uuid, String reason, String executor, String removalReason,
                                   Object removedAt, long time, long until) {
        Map<String, Object> row = new LinkedHashMap<>();
        row.put("id", id);
        row.put("uuid", uuid);
        row.put("reason", reason);
        row.put("banned_by_uuid", executor);
        row.put("removed_by_reason", removalReason);
        row.put("removed_by_date", removedAt);
        row.put("time", time);
        row.put("until", until);
        return row;
    }

    PreparedStatement statement() {
        return (PreparedStatement) Proxy.newProxyInstance(FakeHistory.class.getClassLoader(),
                new Class<?>[]{PreparedStatement.class}, (proxy, method, args) -> switch (method.getName()) {
                    case "setLong" -> {
                        params.put((Integer) args[0], args[1]);
                        yield null;
                    }
                    case "setMaxRows" -> {
                        maxRows = (Integer) args[0];
                        yield null;
                    }
                    case "executeQuery" -> {
                        queryThread = Thread.currentThread();
                        yield results();
                    }
                    case "close" -> {
                        statementClosed = true;
                        yield null;
                    }
                    default -> throw new UnsupportedOperationException(method.getName());
                });
    }

    private ResultSet results() {
        int[] at = {-1};
        return (ResultSet) Proxy.newProxyInstance(FakeHistory.class.getClassLoader(), new Class<?>[]{ResultSet.class},
                (proxy, method, args) -> {
                    switch (method.getName()) {
                        case "next" -> {
                            if (at[0] >= 0) {
                                afterRow.accept(at[0]);
                            }
                            return ++at[0] < rows.size();
                        }
                        case "close" -> {
                            resultClosed = true;
                            return null;
                        }
                        default -> {
                        }
                    }
                    Object value = rows.get(at[0]).get((String) args[0]);
                    if (value instanceof SQLException e) {
                        throw e;
                    }
                    return switch (method.getName()) {
                        case "getLong" -> value == null ? 0L : ((Number) value).longValue();
                        case "getString" -> (String) value;
                        case "getTimestamp" -> (Timestamp) value;
                        default -> throw new UnsupportedOperationException(method.getName());
                    };
                });
    }
}
