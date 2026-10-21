package dev.pandascript.pandaeye.paper.check;

/** Schedules confirmation steps under the per-player and global limits. */
public interface CandidateSink {

    /** Returns false when the candidate was dropped (limits); the sink counts the drop. */
    boolean submit(CheckHandle handle, Candidate candidate);
}
