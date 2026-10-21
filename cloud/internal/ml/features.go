package ml

import (
	"math"
	"slices"
	"sort"
)

// Feature contract v1 (ml/FEATURES.md). Any change to a name, the order, a definition, a default or a
// clip range needs a new FeatureVersion here, in ml/FEATURES.md and in ml/pandaeye_ml/features.py.
const (
	FeatureVersion = 1
	NumFeatures    = 94

	WindowUs = 30_000_000 // window length (normalised microseconds)
	// GraceUs is the lookahead G: items of a window are decided with events up to its end + G.
	GraceUs = 2_000_000
	// HousekeepAfterUs finalises a window once the partition's newest time is this far past its end.
	HousekeepAfterUs = 60_000_000

	tickProtocol = 768 // tick clients (CHECKS.md 0)
)

type featureSpec struct {
	name   string
	def    float32
	lo, hi float32
	tick   bool // computed only for tick clients, else the default
}

// table is ml/FEATURES.md section 4 in order; features_doc_test.go parses the document and compares.
var table = [NumFeatures]featureSpec{
	{"active_seconds", 0, 0, 30, false},
	{"move_count", 0, 0, 3000, false},
	{"focus_share", 0, 0, 1, false},
	{"server_tick_ms_max", 50, 0, 1000, false},
	{"server_stall_ms", 0, 0, 30000, false},
	{"env_special_share", 0, 0, 1, false},
	{"click_count", 0, 0, 2000, true},
	{"click_cps_mean", -1, 0, 60, true},
	{"click_cps_peak", 0, 0, 60, true},
	{"click_interval_count", 0, 0, 2000, true},
	{"click_interval_mean", -1, 1, 10, true},
	{"click_interval_sd", -1, 0, 5, true},
	{"click_interval_skew", 0, -10, 10, true},
	{"click_interval_kurtosis", 0, 1, 100, true},
	{"click_interval_distinct", 0, 1, 10, true},
	{"click_interval_mode_share", 0, 0, 1, true},
	{"click_pattern_share", 0, 0, 1, true},
	{"click_doubles_share", 0, 0, 1, true},
	{"rot_combat_count", 0, 0, 2000, true},
	{"rot_combat_yaw_mean", -1, 0, 180, true},
	{"rot_combat_yaw_sd", -1, 0, 180, true},
	{"rot_combat_pitch_mean", -1, 0, 180, true},
	{"rot_combat_pitch_sd", -1, 0, 180, true},
	{"rot_combat_yaw_accel_mean", -1, 0, 360, true},
	{"rot_idle_count", 0, 0, 2000, true},
	{"rot_idle_yaw_mean", -1, 0, 180, true},
	{"rot_idle_yaw_sd", -1, 0, 180, true},
	{"rot_idle_pitch_mean", -1, 0, 180, true},
	{"rot_idle_pitch_sd", -1, 0, 180, true},
	{"gcd_calibrated", 0, 0, 1, false},
	{"gcd_combat_ongrid_share", -1, 0, 1, true},
	{"gcd_idle_ongrid_share", -1, 0, 1, true},
	{"snap_count", 0, 0, 600, true},
	{"snap_share", 0, 0, 1, true},
	{"aim_precision_mean", -1, 0, 90, false},
	{"aim_precision_sd", -1, 0, 90, false},
	{"attack_count", 0, 0, 2000, false},
	{"attack_eligible_count", 0, 0, 2000, false},
	{"attack_target_distinct", 0, 0, 100, false},
	{"reach_p50", -1, 0, 8, false},
	{"reach_p95", -1, 0, 8, false},
	{"reach_max", -1, 0, 8, false},
	{"hitbox_miss_share", 0, 0, 1, false},
	{"noswing_share", 0, 0, 1, true},
	{"multi_target_ticks", 0, 0, 600, true},
	{"velocity_count", 0, 0, 200, false},
	{"velocity_v_taken_ratio", -1, 0, 2, false},
	{"velocity_vertical_ignored_share", 0, 0, 1, false},
	{"velocity_jump_reset_share", -1, 0, 1, false},
	{"velocity_h_count", 0, 0, 200, false},
	{"velocity_h_taken_ratio", -1, 0, 2, false},
	{"velocity_h_shortfall_share", 0, 0, 1, false},
	{"flag_timer_balance", 0, 0, 100, false},
	{"flag_movement_speed", 0, 0, 100, false},
	{"flag_movement_fly", 0, 0, 100, false},
	{"flag_movement_step", 0, 0, 100, false},
	{"flag_movement_groundspoof", 0, 0, 100, false},
	{"flag_movement_noslow", 0, 0, 100, false},
	{"flag_dig_speed", 0, 0, 100, false},
	{"flag_protocol_pitch", 0, 0, 100, false},
	{"flag_protocol_position", 0, 0, 100, false},
	{"flag_protocol_slot", 0, 0, 100, false},
	{"flag_protocol_selfhit", 0, 0, 100, false},
	{"flag_protocol_cursor", 0, 0, 100, false},
	{"flag_protocol_flood", 0, 0, 100, false},
	{"flag_protocol_abilities", 0, 0, 100, false},
	{"flag_inventory_closed", 0, 0, 100, false},
	{"flag_movement_severity_max", 0, 0, 1, false},
	{"timer_offset_slope", 0, -200, 200, true},
	{"cs_movement_candidate_rate", -1, 0, 1000, false},
	{"cs_movement_discard_share", -1, 0, 1, false},
	{"cs_speed_max_stat", -1, -0.5, 2, false},
	{"cs_fly_max_stat", -1, -0.5, 5, false},
	{"place_count", 0, 0, 1000, false},
	{"place_eligible_count", 0, 0, 1000, false},
	{"place_face_invalid_share", 0, 0, 1, false},
	{"place_ray_miss_share", 0, 0, 1, false},
	{"place_cursor_mismatch_share", 0, 0, 1, false},
	{"place_reach_max", -1, 0, 10, false},
	{"place_peak_per_s", 0, 0, 40, false},
	{"dig_count", 0, 0, 1000, false},
	{"dig_eligible_count", 0, 0, 2000, false},
	{"dig_ray_miss_share", 0, 0, 1, false},
	{"dig_peak_per_s", 0, 0, 60, false},
	{"inv_click_count", 0, 0, 2000, false},
	{"inv_click_interval_min_ms", -1, 0, 5000, false},
	{"inv_move_ticks", 0, 0, 600, true},
	{"inv_rotate_ticks", 0, 0, 600, true},
	{"ping_median_ms", -1, 0, 5000, false},
	{"ping_jitter_ms", -1, 0, 5000, false},
	{"ka_pong_delta_ms", 0, -1000, 5000, false},
	{"ka_paired_count", 0, 0, 100, false},
	{"gap_count", 0, 0, 60, true},
	{"gap_burst_count", 0, 0, 60, true},
}

// Feature indices (table order).
const (
	FActiveSeconds = iota
	FMoveCount
	FFocusShare
	FServerTickMsMax
	FServerStallMs
	FEnvSpecialShare
	FClickCount
	FClickCPSMean
	FClickCPSPeak
	FClickIntervalCount
	FClickIntervalMean
	FClickIntervalSD
	FClickIntervalSkew
	FClickIntervalKurtosis
	FClickIntervalDistinct
	FClickIntervalModeShare
	FClickPatternShare
	FClickDoublesShare
	FRotCombatCount
	FRotCombatYawMean
	FRotCombatYawSD
	FRotCombatPitchMean
	FRotCombatPitchSD
	FRotCombatYawAccelMean
	FRotIdleCount
	FRotIdleYawMean
	FRotIdleYawSD
	FRotIdlePitchMean
	FRotIdlePitchSD
	FGCDCalibrated
	FGCDCombatOnGridShare
	FGCDIdleOnGridShare
	FSnapCount
	FSnapShare
	FAimPrecisionMean
	FAimPrecisionSD
	FAttackCount
	FAttackEligibleCount
	FAttackTargetDistinct
	FReachP50
	FReachP95
	FReachMax
	FHitboxMissShare
	FNoSwingShare
	FMultiTargetTicks
	FVelocityCount
	FVelocityVTakenRatio
	FVelocityVerticalIgnoredShare
	FVelocityJumpResetShare
	FVelocityHCount
	FVelocityHTakenRatio
	FVelocityHShortfallShare
	FFlagTimerBalance
	FFlagMovementSpeed
	FFlagMovementFly
	FFlagMovementStep
	FFlagMovementGroundspoof
	FFlagMovementNoslow
	FFlagDigSpeed
	FFlagProtocolPitch
	FFlagProtocolPosition
	FFlagProtocolSlot
	FFlagProtocolSelfhit
	FFlagProtocolCursor
	FFlagProtocolFlood
	FFlagProtocolAbilities
	FFlagInventoryClosed
	FFlagMovementSeverityMax
	FTimerOffsetSlope
	FCSMovementCandidateRate
	FCSMovementDiscardShare
	FCSSpeedMaxStat
	FCSFlyMaxStat
	FPlaceCount
	FPlaceEligibleCount
	FPlaceFaceInvalidShare
	FPlaceRayMissShare
	FPlaceCursorMismatchShare
	FPlaceReachMax
	FPlacePeakPerS
	FDigCount
	FDigEligibleCount
	FDigRayMissShare
	FDigPeakPerS
	FInvClickCount
	FInvClickIntervalMinMs
	FInvMoveTicks
	FInvRotateTicks
	FPingMedianMs
	FPingJitterMs
	FKAPongDeltaMs
	FKAPairedCount
	FGapCount
	FGapBurstCount
)

// localFlagFeature maps a LocalFlag check id to its count feature (52-66).
var localFlagFeature = map[string]int{
	"timer.balance": FFlagTimerBalance, "movement.speed": FFlagMovementSpeed, "movement.fly": FFlagMovementFly,
	"movement.step": FFlagMovementStep, "movement.groundspoof": FFlagMovementGroundspoof,
	"movement.noslow": FFlagMovementNoslow, "dig.speed": FFlagDigSpeed, "protocol.pitch": FFlagProtocolPitch,
	"protocol.position": FFlagProtocolPosition, "protocol.slot": FFlagProtocolSlot,
	"protocol.selfhit": FFlagProtocolSelfhit, "protocol.cursor": FFlagProtocolCursor,
	"protocol.flood": FFlagProtocolFlood, "protocol.abilities": FFlagProtocolAbilities,
	"inventory.closed": FFlagInventoryClosed,
}

// FeatureNames returns the ordered feature names of FeatureVersion.
func FeatureNames() []string {
	out := make([]string, NumFeatures)
	for i, f := range table {
		out[i] = f.name
	}
	return out
}

// FeatureDefault returns the default of feature i (written as is, never clipped).
func FeatureDefault(i int) float32 { return table[i].def }

// FeatureClip returns the clip range of feature i.
func FeatureClip(i int) (lo, hi float32) { return table[i].lo, table[i].hi }

// FeatureTickOnly reports whether feature i is computed for tick clients only.
func FeatureTickOnly(i int) bool { return table[i].tick }

// Defaults returns a vector of every feature's default.
func Defaults() [NumFeatures]float32 {
	var v [NumFeatures]float32
	for i, f := range table {
		v[i] = f.def
	}
	return v
}

// vec fills a feature vector: set writes a computed value clipped to the feature's range; values that
// are NaN or infinite, tick-only features of non-tick clients and unset features keep the default.
type vec struct {
	v    [NumFeatures]float32
	tick bool
}

func newVec(tick bool) *vec { return &vec{v: Defaults(), tick: tick} }

func (f *vec) set(i int, x float64) {
	if math.IsNaN(x) || math.IsInf(x, 0) || (table[i].tick && !f.tick) {
		return
	}
	lo, hi := float64(table[i].lo), float64(table[i].hi)
	f.v[i] = float32(math.Min(hi, math.Max(lo, x)))
}

// setIf sets feature i when ok (the minimum sample count is met / the denominator is not 0).
func (f *vec) setIf(i int, ok bool, x float64) {
	if ok {
		f.set(i, x)
	}
}

// share sets num/den when den > 0.
func (f *vec) share(i int, num, den int) {
	if den > 0 {
		f.set(i, float64(num)/float64(den))
	}
}

// Statistics of ml/FEATURES.md section 3.

func fMean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func fMoment(xs []float64, k int) float64 {
	m := fMean(xs)
	s := 0.0
	for _, x := range xs {
		s += math.Pow(x-m, float64(k))
	}
	return s / float64(len(xs))
}

// fSD is the population standard deviation.
func fSD(xs []float64) float64 { return math.Sqrt(fMoment(xs, 2)) }

// fSkew is m3 / m2^1.5; ok is false when m2 < 1e-12.
func fSkew(xs []float64) (float64, bool) {
	m2 := fMoment(xs, 2)
	if m2 < 1e-12 {
		return 0, false
	}
	return fMoment(xs, 3) / math.Pow(m2, 1.5), true
}

// fKurtosis is the Pearson kurtosis m4 / m2^2; ok is false when m2 < 1e-12.
func fKurtosis(xs []float64) (float64, bool) {
	m2 := fMoment(xs, 2)
	if m2 < 1e-12 {
		return 0, false
	}
	return fMoment(xs, 4) / (m2 * m2), true
}

// fMedian is the middle element of the sorted values (mean of the two middle ones for even n).
func fMedian(xs []float64) float64 {
	s := slices.Clone(xs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// fPercentile is the nearest-rank percentile: sorted ascending, element at rank ceil(q/100 * n).
func fPercentile(xs []float64, q float64) float64 {
	s := slices.Clone(xs)
	sort.Float64s(s)
	r := int(math.Ceil(q / 100 * float64(len(s))))
	r = max(1, min(r, len(s)))
	return s[r-1]
}

func fMax(xs []float64) float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}

// wrap180 is a - 360 * round(a / 360).
func wrap180(a float64) float64 { return a - 360*math.Round(a/360) }
