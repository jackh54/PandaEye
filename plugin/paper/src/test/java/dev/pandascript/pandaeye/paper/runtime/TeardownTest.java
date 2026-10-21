package dev.pandascript.pandaeye.paper.runtime;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class TeardownTest {

    @Test
    void aFailingStepNeverSkipsTheStepsAfterIt() {
        List<String> ran = new ArrayList<>();
        List<String> warnings = new ArrayList<>();
        Teardown teardown = new Teardown(warnings::add);
        teardown.step("unregister", () -> ran.add("unregister"));
        teardown.step("cancel tasks", () -> {
            throw new IllegalStateException("scheduler gone");
        });
        teardown.step("stop threads", () -> {
            throw new NoClassDefFoundError("closed");
        });
        teardown.step("write spool", () -> ran.add("spool"));
        assertEquals(List.of("unregister", "spool"), ran);
        assertEquals(2, teardown.failures());
        assertTrue(warnings.get(0).contains("cancel tasks"));
    }

    @Test
    void aFailingWarnDoesNotEscape() {
        Teardown teardown = new Teardown(message -> {
            throw new IllegalStateException("logger gone");
        });
        teardown.step("anything", () -> {
            throw new IllegalStateException("x");
        });
        assertEquals(1, teardown.failures());
    }
}
