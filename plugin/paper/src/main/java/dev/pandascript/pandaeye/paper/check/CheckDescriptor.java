package dev.pandascript.pandaeye.paper.check;

import dev.pandascript.pandaeye.protocol.v1.Category;

/**
 * Static facts about one local check: the scoring row of CHECKS.md section 4 (what the cloud does with its
 * flags) plus the plugin-side violation level rules of section 1.
 *
 * @param points           the "Points" cell of CHECKS.md section 4, verbatim (e.g. {@code 2..6})
 * @param vlDecaySeconds   seconds for the local VL to decay by 1
 * @param setbackVl        VL at which a confirmed violation sets the player back in live mode; 0 = never
 *                         (except while a SETBACK mitigation is active)
 */
public record CheckDescriptor(
        String id,
        Category category,
        String points,
        double confidence,
        int halfLifeSeconds,
        int cap,
        boolean certain,
        double vlDecaySeconds,
        double setbackVl) {

    /** The family is the id prefix before the first dot. */
    public String family() {
        int dot = id.indexOf('.');
        return dot < 0 ? id : id.substring(0, dot);
    }

    public boolean movement() {
        return category == Category.CATEGORY_MOVEMENT || category == Category.CATEGORY_TICK_SPEED;
    }
}
