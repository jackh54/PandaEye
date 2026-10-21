package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.protocol.v1.Category;
import org.junit.jupiter.api.Test;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * {@link CheckCatalog} must match docs/CHECKS.md: the local rows of the section 4 scoring table, and the VL
 * decay and live setback VL stated in each check's section 1 text.
 */
class CheckCatalogTest {

    private static final Path DOC = Path.of(System.getProperty("pandaeye.checksDoc", "../../docs/CHECKS.md"));
    private static final Pattern SECTION = Pattern.compile("(?m)^### 1\\.\\w+ (\\S+) - ");
    private static final Pattern DECAY = Pattern.compile("decay\\s+1\\s+per\\s+(\\d+)\\s+s");
    private static final Pattern SETBACK = Pattern.compile("Live\\s+setback\\s+at\\s+VL\\s+>=\\s+(\\d+)");

    @Test
    void localRowsOfTheScoringTableMatchTheCatalog() throws Exception {
        Map<String, String[]> rows = localRows(Files.readString(DOC));
        assertEquals(CheckCatalog.all().size(), rows.size(), "local rows in CHECKS.md section 4: " + rows.keySet());
        for (CheckDescriptor check : CheckCatalog.all()) {
            String[] row = rows.get(check.id());
            assertTrue(row != null, check.id() + " missing from CHECKS.md section 4");
            assertEquals(Category.valueOf("CATEGORY_" + row[2]), check.category(), check.id() + " category");
            assertEquals(row[3], check.points(), check.id() + " points");
            assertEquals(Double.parseDouble(row[4]), check.confidence(), 1e-9, check.id() + " confidence");
            assertEquals(Integer.parseInt(row[5]), check.halfLifeSeconds(), check.id() + " half-life");
            assertEquals(Integer.parseInt(row[6]), check.cap(), check.id() + " cap");
            assertEquals(row[7].equals("yes"), check.certain(), check.id() + " certain");
        }
    }

    @Test
    void decayAndSetbackMatchSectionOne() throws Exception {
        Map<String, String> sections = sectionOne(Files.readString(DOC));
        for (CheckDescriptor check : CheckCatalog.all()) {
            String text = sections.get(check.id());
            assertTrue(text != null, check.id() + " has no CHECKS.md section 1 entry");
            Matcher decay = DECAY.matcher(text);
            if (decay.find()) {
                assertEquals(Double.parseDouble(decay.group(1)), check.vlDecaySeconds(), 1e-9, check.id() + " decay");
            }
            Matcher setback = SETBACK.matcher(text);
            if (setback.find()) {
                assertEquals(Double.parseDouble(setback.group(1)), check.setbackVl(), 1e-9, check.id() + " setback VL");
            } else {
                assertEquals(0, check.setbackVl(), check.id() + " never sets back");
            }
        }
    }

    @Test
    void familiesAndLookups() {
        assertEquals("movement", CheckCatalog.get(CheckCatalog.MOVEMENT_SPEED).family());
        assertTrue(CheckCatalog.get(CheckCatalog.TIMER_BALANCE).movement());
        assertFalse(CheckCatalog.get(CheckCatalog.PROTOCOL_PITCH).movement());
        assertFalse(CheckCatalog.contains("reach.distance"));
    }

    /** id -> cells of the "| check | local | ..." rows of section 4. */
    private static Map<String, String[]> localRows(String doc) {
        String section = doc.substring(doc.indexOf("## 4. Scoring summary"));
        Map<String, String[]> rows = new LinkedHashMap<>();
        for (String line : section.split("\n")) {
            if (!line.startsWith("| ")) {
                continue;
            }
            String[] cells = line.substring(1, line.length() - 1).split("\\|", -1);
            for (int i = 0; i < cells.length; i++) {
                cells[i] = cells[i].trim();
            }
            if (cells.length >= 8 && cells[1].equals("local")) {
                rows.put(cells[0], cells);
            }
        }
        return rows;
    }

    /** id -> the text of its "### 1.x id - CATEGORY" section. */
    private static Map<String, String> sectionOne(String doc) {
        String section = doc.substring(doc.indexOf("## 1. Local checks"), doc.indexOf("## 2. Cloud checks"));
        Matcher matcher = SECTION.matcher(section);
        List<int[]> starts = new java.util.ArrayList<>();
        List<String> ids = new java.util.ArrayList<>();
        while (matcher.find()) {
            starts.add(new int[]{matcher.start()});
            ids.add(matcher.group(1));
        }
        Map<String, String> out = new LinkedHashMap<>();
        for (int i = 0; i < ids.size(); i++) {
            int end = i + 1 < ids.size() ? starts.get(i + 1)[0] : section.length();
            out.put(ids.get(i), section.substring(starts.get(i)[0], end));
        }
        return out;
    }
}
