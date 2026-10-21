// @ts-check
/*
 * PandaEye replay viewer. Loads a clip (GET <data-clip-url>, proxied by the Worker from the API's
 * GET /api/v1/clips/{id}) and draws it on <canvas>: a top-down view and a side view of the suspect's path
 * and the attack targets, rotations as view rays, attacks / block placements / digging / velocity /
 * teleports as markers, with a timeline scrubber and play/pause.
 *
 * Security: clip contents are untrusted. Text reaches the page only through textContent and the canvas
 * only through fillText; no markup is ever parsed from clip data.
 *
 * Accepted clip JSON (protojson of ClipData, snake_case or camelCase, 64-bit numbers as numbers or strings,
 * UUIDs as hyphenated strings or base64 bytes):
 *   {player, player_name?, start_us?, end_us?, events: [{player?, time_us, tick?, <kind>: {...}}]}
 * where <kind> is one of the Event oneof fields of proto/pandaeye/v1/wire.proto (move, attack, swing,
 * place, dig, velocity, teleport, env, flag, ...), or {kind: "<kind>", <kind>: {...}}.
 */
(function (root) {
  "use strict";

  var MAX_EVENTS = 200000;
  var EYE_HEIGHT = 1.62;
  var EYE_HEIGHT_SNEAK = 1.27;
  var RAY_LENGTH = 3.0;
  var TRAIL_MS = 2000;

  var KINDS = [
    "move", "attack", "swing", "place", "dig", "velocity", "teleport", "env", "flag", "click", "latency",
    "state", "join", "quit", "probe", "use_item", "server_switch", "stall", "check_stats",
  ];

  var MOVE_FLAGS = [
    [1, "sprinting"], [2, "sneaking"], [4, "swimming"], [8, "gliding"], [16, "in vehicle"], [32, "flying"],
    [64, "riptide"], [128, "using item"], [256, "teleport pending"], [512, "velocity pending"],
    [1024, "container open"], [2048, "dead"], [4096, "server rotation"], [8192, "cancelled"], [16384, "scoping"],
  ];

  var ENV_FLAGS = [
    [1, "in water"], [2, "in lava"], [4, "climbing"], [8, "on ice"], [16, "on slime"], [32, "in web"],
    [64, "on honey"], [128, "in powder snow"], [256, "bubble column"], [512, "soul speed block"],
    [1024, "near solid"], [2048, "in vehicle"], [4096, "gliding"], [8192, "riptiding"], [16384, "swimming"],
    [32768, "near entity"], [65536, "low ceiling"], [131072, "stuck"], [262144, "extended reach item"],
  ];

  var FACES = ["down", "up", "north", "south", "west", "east", "other"];
  var DIG_ACTIONS = ["start", "cancel", "finish", "other"];

  // ---- untrusted JSON helpers ---------------------------------------------------------------------

  function camel(s) {
    return s.replace(/_([a-z])/g, function (_m, ch) {
      return ch.toUpperCase();
    });
  }

  function get(o, name) {
    if (!o || typeof o !== "object") return undefined;
    var v = o[name];
    if (v === undefined || v === null) v = o[camel(name)];
    return v === null ? undefined : v;
  }

  function num(v, d) {
    var n = typeof v === "number" ? v : typeof v === "string" && v.trim() !== "" ? Number(v) : NaN;
    return isFinite(n) ? n : d;
  }

  function bool(v) {
    return v === true || v === 1 || v === "true";
  }

  function str(v, max) {
    if (typeof v === "string") return v.slice(0, max || 256);
    if (typeof v === "number" && isFinite(v)) return String(v);
    return "";
  }

  function arr(v) {
    return Array.isArray(v) ? v : [];
  }

  function vec(o) {
    if (!o || typeof o !== "object") return null;
    var x = num(get(o, "x"), NaN);
    var y = num(get(o, "y"), NaN);
    var z = num(get(o, "z"), NaN);
    return isFinite(x) && isFinite(y) && isFinite(z) ? { x: x, y: y, z: z } : null;
  }

  /** Proto enum value as an index: accepts numbers and names like "FACE_UP" / "ACTION_FINISH". */
  function enumIndex(v, names, prefix) {
    if (typeof v === "number" && isFinite(v)) return v;
    if (typeof v === "string") {
      var s = v.toLowerCase().replace(prefix, "");
      var i = names.indexOf(s);
      return i;
    }
    return 0;
  }

  /** UUID from a hyphenated / 32-hex string or from base64 of 16 bytes (protojson bytes). */
  function uuidOf(v) {
    if (typeof v !== "string" || !v) return "";
    var s = v.trim();
    if (/^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$/.test(s)) {
      var h = s.replace(/-/g, "").toLowerCase();
      return h.slice(0, 8) + "-" + h.slice(8, 12) + "-" + h.slice(12, 16) + "-" + h.slice(16, 20) + "-" + h.slice(20);
    }
    if (/^[A-Za-z0-9+/_-]{21,24}={0,2}$/.test(s)) {
      try {
        var bin = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
        if (bin.length === 16) {
          var hex = "";
          for (var i = 0; i < 16; i++) hex += ("0" + bin.charCodeAt(i).toString(16)).slice(-2);
          return uuidOf(hex);
        }
      } catch (_e) {
        /* not base64 */
      }
    }
    return s.slice(0, 64);
  }

  function eventKind(e) {
    var k = get(e, "kind");
    if (typeof k === "string" && KINDS.indexOf(k) >= 0) {
      var b = get(e, k);
      return [k, b && typeof b === "object" ? b : get(e, "data") || e];
    }
    for (var i = 0; i < KINDS.length; i++) {
      var body = get(e, KINDS[i]);
      if (body && typeof body === "object") return [KINDS[i], body];
    }
    return [null, null];
  }

  function byTime(a, b) {
    return a.t - b.t;
  }

  /** Normalises untrusted clip JSON into sorted, typed tracks. Times are milliseconds on the plugin clock. */
  function normalizeClip(raw) {
    var clip = raw && typeof raw === "object" ? raw : {};
    if (get(clip, "clip") && typeof get(clip, "clip") === "object" && !Array.isArray(get(clip, "events"))) clip = get(clip, "clip");
    var dataObj = get(clip, "data");
    var events = arr(get(clip, "events"));
    if (!events.length && dataObj && typeof dataObj === "object") events = arr(get(dataObj, "events"));
    var suspect = uuidOf(get(clip, "player") || get(clip, "player_uuid") || (dataObj && get(dataObj, "player")) || "");
    if (!suspect) {
      // Most frequent event player.
      // Keyed by untrusted strings: no prototype, so "__proto__" / "constructor" are ordinary keys.
      var counts = Object.create(null);
      var best = "";
      for (var c = 0; c < Math.min(events.length, 5000); c++) {
        var pu = uuidOf(get(events[c], "player"));
        if (!pu) continue;
        counts[pu] = (counts[pu] || 0) + 1;
        if (!best || counts[pu] > counts[best]) best = pu;
      }
      suspect = best;
    }

    var out = {
      player: suspect,
      playerName: str(get(clip, "player_name"), 32),
      moves: [],
      attacks: [],
      swings: [],
      places: [],
      digs: [],
      velocities: [],
      teleports: [],
      envs: [],
      flags: [],
      // Keyed by untrusted target ids: no prototype (a "__proto__" or "constructor" id must not hit Object.prototype).
      targets: Object.create(null),
      start: Infinity,
      end: -Infinity,
      skipped: 0,
    };

    function seen(t) {
      if (t < out.start) out.start = t;
      if (t > out.end) out.end = t;
    }

    function pushMove(t, body) {
      var hasPosRaw = get(body, "has_pos");
      var hasRotRaw = get(body, "has_rot");
      var x = num(get(body, "x"), NaN);
      var y = num(get(body, "y"), NaN);
      var z = num(get(body, "z"), NaN);
      var hasPos = (hasPosRaw === undefined ? true : bool(hasPosRaw)) && isFinite(x) && isFinite(y) && isFinite(z);
      var yaw = num(get(body, "yaw"), NaN);
      var pitch = num(get(body, "pitch"), NaN);
      var hasRot = (hasRotRaw === undefined ? true : bool(hasRotRaw)) && isFinite(yaw) && isFinite(pitch);
      out.moves.push({
        t: t,
        x: x,
        y: y,
        z: z,
        yaw: yaw,
        pitch: pitch,
        hasPos: hasPos,
        hasRot: hasRot,
        onGround: bool(get(body, "on_ground")),
        flags: num(get(body, "flags"), 0),
      });
    }

    var n = Math.min(events.length, MAX_EVENTS);
    out.skipped = events.length - n;
    for (var i = 0; i < n; i++) {
      var e = events[i];
      if (!e || typeof e !== "object") continue;
      var t = num(get(e, "time_us"), NaN) / 1000;
      if (!isFinite(t)) continue;
      var ep = uuidOf(get(e, "player"));
      if (suspect && ep && ep !== suspect) continue;
      var kb = eventKind(e);
      var kind = kb[0];
      var body = kb[1];
      if (!kind) continue;
      seen(t);
      switch (kind) {
        case "move":
          pushMove(t, body);
          break;
        case "attack": {
          var tp = uuidOf(get(body, "target_player"));
          var key = tp || "entity " + num(get(body, "target_entity_id"), 0);
          var history = arr(get(body, "target_history"))
            .slice(0, 256)
            .map(function (h) {
              return { t: num(get(h, "time_us"), NaN) / 1000, x: num(get(h, "x"), NaN), y: num(get(h, "y"), NaN), z: num(get(h, "z"), NaN) };
            })
            .filter(function (h) {
              return isFinite(h.t) && isFinite(h.x) && isFinite(h.y) && isFinite(h.z);
            });
          var width = num(get(body, "target_width"), 0) || 0.6;
          var height = num(get(body, "target_height"), 0) || 1.8;
          var target = out.targets[key];
          if (!target) {
            target = out.targets[key] = { key: key, type: str(get(body, "target_type"), 64), width: width, height: height, positions: [] };
          }
          for (var h = 0; h < history.length; h++) target.positions.push(history[h]);
          out.attacks.push({
            t: t,
            eye: vec(get(body, "eye")),
            yaw: num(get(body, "yaw"), NaN),
            pitch: num(get(body, "pitch"), NaN),
            target: key,
            targetType: str(get(body, "target_type"), 64),
            width: width,
            height: height,
            history: history,
            pingMs: num(get(body, "ping_ms"), NaN),
            reach: num(get(body, "entity_reach"), 3),
            sprinting: bool(get(body, "sprinting")),
            stab: bool(get(body, "stab")),
          });
          break;
        }
        case "swing":
          out.swings.push({ t: t, offhand: enumIndex(get(body, "hand"), ["main", "off"], /^hand_/) === 1 });
          break;
        case "place":
          out.places.push({
            t: t,
            block: vec(get(body, "block")),
            face: FACES[enumIndex(get(body, "face"), FACES, /^face_/)] || "other",
            eye: vec(get(body, "eye")),
            yaw: num(get(body, "yaw"), NaN),
            pitch: num(get(body, "pitch"), NaN),
            item: str(get(body, "item"), 64),
          });
          break;
        case "dig":
          out.digs.push({
            t: t,
            action: DIG_ACTIONS[enumIndex(get(body, "action"), DIG_ACTIONS, /^action_/)] || "other",
            block: vec(get(body, "block")),
            face: FACES[enumIndex(get(body, "face"), FACES, /^face_/)] || "other",
            eye: vec(get(body, "eye")),
            yaw: num(get(body, "yaw"), NaN),
            pitch: num(get(body, "pitch"), NaN),
          });
          break;
        case "velocity":
          out.velocities.push({
            t: t,
            vx: num(get(body, "vx"), 0),
            vy: num(get(body, "vy"), 0),
            vz: num(get(body, "vz"), 0),
            explosion: enumIndex(get(body, "source"), ["velocity_packet", "explosion"], /^source_/) === 1,
          });
          break;
        case "teleport": {
          var tpos = vec(body);
          if (tpos) out.teleports.push({ t: t, x: tpos.x, y: tpos.y, z: tpos.z, confirmed: bool(get(body, "confirmed")) });
          break;
        }
        case "env":
          out.envs.push({ t: t, flags: num(get(body, "flags"), 0), pos: vec(body), tickMs: num(get(body, "server_tick_ms"), NaN) });
          break;
        case "flag": {
          out.flags.push({
            t: t,
            check: str(get(body, "check_id"), 64),
            info: str(get(body, "info"), 200),
            vl: num(get(body, "vl"), NaN),
            confidence: num(get(body, "confidence"), NaN),
          });
          var ctx = arr(get(body, "context")).slice(0, 64);
          for (var k = 0; k < ctx.length; k++) {
            var ct = num(get(ctx[k], "time_us"), NaN) / 1000;
            var mv = get(ctx[k], "move");
            if (isFinite(ct) && mv && typeof mv === "object") {
              pushMove(ct, mv);
              seen(ct);
            }
          }
          break;
        }
        default:
          break;
      }
    }

    out.moves.sort(byTime);
    // De-duplicate moves (flag context repeats the live samples).
    var dedup = [];
    for (var m = 0; m < out.moves.length; m++) {
      var prev = dedup[dedup.length - 1];
      var cur = out.moves[m];
      if (prev && Math.abs(prev.t - cur.t) < 0.001 && prev.x === cur.x && prev.z === cur.z) continue;
      dedup.push(cur);
    }
    out.moves = dedup;
    [out.attacks, out.swings, out.places, out.digs, out.velocities, out.teleports, out.envs, out.flags].forEach(function (a) {
      a.sort(byTime);
    });
    Object.keys(out.targets).forEach(function (key) {
      var ps = out.targets[key].positions.sort(byTime);
      var uniq = [];
      for (var q = 0; q < ps.length; q++) {
        if (uniq.length && Math.abs(uniq[uniq.length - 1].t - ps[q].t) < 0.001) continue;
        uniq.push(ps[q]);
        seen(ps[q].t);
      }
      out.targets[key].positions = uniq;
    });
    var cs = num(get(clip, "start_us"), NaN) / 1000;
    var ce = num(get(clip, "end_us"), NaN) / 1000;
    if (isFinite(cs) && cs < out.start) out.start = cs;
    if (isFinite(ce) && ce > out.end) out.end = ce;
    if (!isFinite(out.start)) {
      out.start = 0;
      out.end = 0;
    }
    out.positions = out.moves.filter(function (p) {
      return p.hasPos;
    });
    out.rotations = out.moves.filter(function (p) {
      return p.hasRot;
    });
    return out;
  }

  // ---- geometry -------------------------------------------------------------------------------------

  /** Minecraft view direction: yaw 0 = +z (south), yaw 90 = -x (west); pitch > 0 looks down. */
  function viewDirection(yawDeg, pitchDeg) {
    var yaw = (yawDeg * Math.PI) / 180;
    var pitch = (pitchDeg * Math.PI) / 180;
    return { x: -Math.sin(yaw) * Math.cos(pitch), y: -Math.sin(pitch), z: Math.cos(yaw) * Math.cos(pitch) };
  }

  /** Index of the last element with t <= time (-1 when none). */
  function lastAt(list, time) {
    var lo = 0;
    var hi = list.length - 1;
    var ans = -1;
    while (lo <= hi) {
      var mid = (lo + hi) >> 1;
      if (list[mid].t <= time) {
        ans = mid;
        lo = mid + 1;
      } else hi = mid - 1;
    }
    return ans;
  }

  function positionAt(list, time) {
    var i = lastAt(list, time);
    if (i < 0) return list.length && list[0].t - time < 50 ? list[0] : null;
    var a = list[i];
    var b = list[i + 1];
    if (!b || b.t - a.t > 1000 || b.t === a.t) return a;
    var f = (time - a.t) / (b.t - a.t);
    return { t: time, x: a.x + (b.x - a.x) * f, y: a.y + (b.y - a.y) * f, z: a.z + (b.z - a.z) * f };
  }

  function flagNames(bits, table) {
    var names = [];
    for (var i = 0; i < table.length; i++) if (bits & table[i][0]) names.push(table[i][1]);
    return names;
  }

  // ---- viewer ---------------------------------------------------------------------------------------

  function Viewer(el) {
    this.el = el;
    this.url = el.getAttribute("data-clip-url") || "";
    this.top = el.querySelector("canvas.replay-top");
    this.side = el.querySelector("canvas.replay-side");
    this.timeline = el.querySelector("canvas.replay-timeline");
    this.statusEl = el.querySelector(".replay-status");
    this.playBtn = el.querySelector(".replay-play");
    this.prevBtn = el.querySelector(".replay-prev");
    this.nextBtn = el.querySelector(".replay-next");
    this.scrub = el.querySelector(".replay-scrub");
    this.timeEl = el.querySelector(".replay-time");
    this.speedEl = el.querySelector(".replay-speed");
    this.sideCaption = el.querySelector(".replay-side-caption");
    this.fields = {};
    var dds = el.querySelectorAll("[data-field]");
    for (var i = 0; i < dds.length; i++) this.fields[dds[i].getAttribute("data-field")] = dds[i];
    this.clip = null;
    this.t = 0;
    this.playing = false;
    this.lastFrame = 0;
    this.colors = {};
  }

  Viewer.prototype.setStatus = function (text) {
    if (this.statusEl) this.statusEl.textContent = text;
  };

  Viewer.prototype.readColors = function () {
    var cs = getComputedStyle(this.el);
    var names = ["bg", "grid", "grid-strong", "text", "suspect", "target", "attack", "place", "dig", "velocity", "teleport", "flag"];
    for (var i = 0; i < names.length; i++) this.colors[names[i]] = cs.getPropertyValue("--rp-" + names[i]).trim() || "#888";
  };

  Viewer.prototype.load = function () {
    var self = this;
    if (!this.url || this.url.charAt(0) !== "/") {
      this.setStatus("No clip to load.");
      return;
    }
    fetch(this.url, { credentials: "same-origin", headers: { Accept: "application/json" } })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        return res.json();
      })
      .then(function (json) {
        self.init(normalizeClip(json));
      })
      .catch(function (err) {
        self.setStatus("The clip could not be loaded (" + String(err && err.message ? err.message : err).slice(0, 80) + ").");
      });
  };

  Viewer.prototype.init = function (clip) {
    this.clip = clip;
    if (!clip.positions.length && !clip.attacks.length && !clip.places.length) {
      this.setStatus("This clip contains no positions to draw.");
      return;
    }
    this.computeGeometry();
    this.readColors();
    var duration = Math.max(1, Math.round(clip.end - clip.start));
    this.scrub.max = String(duration);
    this.scrub.value = "0";
    this.t = clip.start;
    var targetCount = Object.keys(clip.targets).length;
    this.setStatus(
      (duration / 1000).toFixed(1) +
        " s: " +
        clip.positions.length +
        " positions, " +
        clip.attacks.length +
        " attacks on " +
        targetCount +
        " target" +
        (targetCount === 1 ? "" : "s") +
        ", " +
        clip.swings.length +
        " swings, " +
        clip.places.length +
        " placements, " +
        clip.digs.length +
        " dig events, " +
        clip.velocities.length +
        " velocity, " +
        clip.teleports.length +
        " teleports." +
        (clip.skipped ? " (" + clip.skipped + " events beyond the viewer limit were skipped.)" : ""),
    );
    var buttons = [this.playBtn, this.prevBtn, this.nextBtn, this.scrub];
    for (var i = 0; i < buttons.length; i++) if (buttons[i]) buttons[i].disabled = false;
    this.bind();
    this.resize();
  };

  Viewer.prototype.computeGeometry = function () {
    var clip = this.clip;
    var pts = [];
    clip.positions.forEach(function (p) {
      pts.push(p);
    });
    Object.keys(clip.targets).forEach(function (k) {
      clip.targets[k].positions.forEach(function (p) {
        pts.push(p);
      });
    });
    clip.attacks.forEach(function (a) {
      if (a.eye) pts.push(a.eye);
    });
    clip.places.concat(clip.digs).forEach(function (p) {
      if (p.block) pts.push({ x: p.block.x + 0.5, y: p.block.y + 0.5, z: p.block.z + 0.5 });
    });
    clip.teleports.forEach(function (p) {
      pts.push(p);
    });
    var minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity, minZ = Infinity, maxZ = -Infinity;
    var sx = 0, sz = 0;
    pts.forEach(function (p) {
      minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x);
      minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y + 2);
      minZ = Math.min(minZ, p.z); maxZ = Math.max(maxZ, p.z);
      sx += p.x; sz += p.z;
    });
    var cx = sx / pts.length, cz = sz / pts.length;
    // Principal horizontal axis (2x2 covariance) for the side view.
    var xx = 0, zz = 0, xz = 0;
    pts.forEach(function (p) {
      var dx = p.x - cx, dz = p.z - cz;
      xx += dx * dx; zz += dz * dz; xz += dx * dz;
    });
    var angle = 0.5 * Math.atan2(2 * xz, xx - zz);
    var ux = Math.cos(angle), uz = Math.sin(angle);
    var minS = Infinity, maxS = -Infinity;
    pts.forEach(function (p) {
      var s = (p.x - cx) * ux + (p.z - cz) * uz;
      minS = Math.min(minS, s); maxS = Math.max(maxS, s);
    });
    function padRange(lo, hi, min) {
      var span = Math.max(hi - lo, min);
      var mid = (lo + hi) / 2;
      return [mid - span / 2 - 1, mid + span / 2 + 1];
    }
    this.geo = {
      x: padRange(minX, maxX, 6),
      z: padRange(minZ, maxZ, 6),
      y: padRange(minY, maxY, 4),
      s: padRange(minS, maxS, 6),
      cx: cx, cz: cz, ux: ux, uz: uz,
    };
    if (this.sideCaption) {
      var deg = Math.round(((Math.atan2(uz, ux) * 180) / Math.PI + 360) % 180);
      this.sideCaption.textContent = "Side view (height over the main horizontal axis, " + deg + "° from +x)";
    }
  };

  Viewer.prototype.bind = function () {
    var self = this;
    this.playBtn.addEventListener("click", function () {
      self.setPlaying(!self.playing);
    });
    this.scrub.addEventListener("input", function () {
      self.setPlaying(false);
      self.t = self.clip.start + Number(self.scrub.value);
      self.draw();
    });
    this.prevBtn.addEventListener("click", function () {
      self.jump(-1);
    });
    this.nextBtn.addEventListener("click", function () {
      self.jump(1);
    });
    this.el.addEventListener("keydown", function (ev) {
      if (ev.target && /** @type {HTMLElement} */ (ev.target).tagName === "SELECT") return;
      if (ev.key === " " && ev.target !== self.playBtn) {
        ev.preventDefault();
        self.setPlaying(!self.playing);
      }
    });
    var dragging = false;
    function seekFromEvent(ev) {
      var rect = self.timeline.getBoundingClientRect();
      var f = Math.min(1, Math.max(0, (ev.clientX - rect.left) / Math.max(1, rect.width)));
      self.setPlaying(false);
      self.t = self.clip.start + f * (self.clip.end - self.clip.start);
      self.draw();
    }
    this.timeline.addEventListener("pointerdown", function (ev) {
      dragging = true;
      self.timeline.setPointerCapture(ev.pointerId);
      seekFromEvent(ev);
    });
    this.timeline.addEventListener("pointermove", function (ev) {
      if (dragging) seekFromEvent(ev);
    });
    this.timeline.addEventListener("pointerup", function () {
      dragging = false;
    });
    if (typeof ResizeObserver !== "undefined") {
      new ResizeObserver(function () {
        self.resize();
      }).observe(this.el);
    } else {
      window.addEventListener("resize", function () {
        self.resize();
      });
    }
    if (window.matchMedia) {
      var mq = window.matchMedia("(prefers-color-scheme: dark)");
      var onChange = function () {
        self.readColors();
        self.draw();
      };
      if (mq.addEventListener) mq.addEventListener("change", onChange);
    }
  };

  Viewer.prototype.interestingTimes = function () {
    if (this._times) return this._times;
    var c = this.clip;
    var times = [];
    [c.attacks, c.places, c.digs, c.velocities, c.teleports, c.flags].forEach(function (list) {
      list.forEach(function (e) {
        times.push(e.t);
      });
    });
    times.sort(function (a, b) {
      return a - b;
    });
    this._times = times;
    return times;
  };

  Viewer.prototype.jump = function (dir) {
    this.setPlaying(false);
    var times = this.interestingTimes();
    var t = this.t;
    var target = null;
    if (dir > 0) {
      for (var i = 0; i < times.length; i++) if (times[i] > t + 1) { target = times[i]; break; }
    } else {
      for (var j = times.length - 1; j >= 0; j--) if (times[j] < t - 1) { target = times[j]; break; }
    }
    if (target !== null) {
      this.t = target;
      this.draw();
    }
  };

  Viewer.prototype.setPlaying = function (on) {
    var self = this;
    if (on && this.t >= this.clip.end) this.t = this.clip.start;
    this.playing = on;
    this.playBtn.textContent = on ? "Pause" : "Play";
    this.playBtn.setAttribute("aria-pressed", on ? "true" : "false");
    if (on) {
      this.lastFrame = 0;
      requestAnimationFrame(function step(ts) {
        if (!self.playing) return;
        if (self.lastFrame) {
          var speed = Number(self.speedEl ? self.speedEl.value : 1) || 1;
          self.t += (ts - self.lastFrame) * speed;
        }
        self.lastFrame = ts;
        if (self.t >= self.clip.end) {
          self.t = self.clip.end;
          self.draw();
          self.setPlaying(false);
          return;
        }
        self.draw();
        requestAnimationFrame(step);
      });
    }
  };

  Viewer.prototype.resize = function () {
    var dpr = Math.min(window.devicePixelRatio || 1, 3);
    [this.top, this.side, this.timeline].forEach(function (cv) {
      var w = cv.clientWidth || cv.width;
      var ratio = cv.height / cv.width || 0.68;
      if (!cv._ratio) cv._ratio = ratio;
      var h = cv.classList.contains("replay-timeline") ? 64 : Math.round(w * cv._ratio);
      cv.style.height = h + "px";
      cv.width = Math.max(1, Math.round(w * dpr));
      cv.height = Math.max(1, Math.round(h * dpr));
      cv._dpr = dpr;
    });
    this.draw();
  };

  Viewer.prototype.draw = function () {
    if (!this.clip || !this.geo) return;
    this.scrub.value = String(Math.round(this.t - this.clip.start));
    this.timeEl.textContent = ((this.t - this.clip.start) / 1000).toFixed(3) + " s";
    var g = this.geo;
    var self = this;
    this.drawView(this.top, function (p) {
      return [p.x, p.z];
    }, g.x, g.z, false, "x", "z");
    this.drawView(this.side, function (p) {
      return [(p.x - g.cx) * g.ux + (p.z - g.cz) * g.uz, p.y];
    }, g.s, g.y, true, "", "y");
    this.drawTimeline();
    this.updateInfo();
    void self;
  };

  /** Draws one projection. `proj` maps a world point to [h, v]; v grows downward unless flipV. */
  Viewer.prototype.drawView = function (cv, proj, hRange, vRange, flipV, hLabel, vLabel) {
    var ctx = cv.getContext("2d");
    if (!ctx) return;
    var dpr = cv._dpr || 1;
    var W = cv.width / dpr, H = cv.height / dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var C = this.colors;
    ctx.fillStyle = C.bg;
    ctx.fillRect(0, 0, W, H);
    var pad = 22;
    var scale = Math.min((W - 2 * pad) / (hRange[1] - hRange[0]), (H - 2 * pad) / (vRange[1] - vRange[0]));
    var hMid = (hRange[0] + hRange[1]) / 2, vMid = (vRange[0] + vRange[1]) / 2;
    function toPx(h, v) {
      var px = W / 2 + (h - hMid) * scale;
      var py = flipV ? H / 2 - (v - vMid) * scale : H / 2 + (v - vMid) * scale;
      return [px, py];
    }
    function P(p) {
      var hv = proj(p);
      return toPx(hv[0], hv[1]);
    }

    // Grid
    var steps = [0.5, 1, 2, 5, 10, 20, 50, 100, 200, 500];
    var step = steps[steps.length - 1];
    for (var si = 0; si < steps.length; si++) if (steps[si] * scale >= 28) { step = steps[si]; break; }
    ctx.lineWidth = 1;
    ctx.font = "10px system-ui, sans-serif";
    ctx.fillStyle = C.text;
    var hVisible = [hMid - W / 2 / scale, hMid + W / 2 / scale];
    var vVisible = [vMid - H / 2 / scale, vMid + H / 2 / scale];
    for (var gh = Math.ceil(hVisible[0] / step) * step; gh <= hVisible[1]; gh += step) {
      var x = toPx(gh, vMid)[0];
      ctx.strokeStyle = Math.abs(gh % (step * 5)) < 1e-9 ? C["grid-strong"] : C.grid;
      ctx.beginPath(); ctx.moveTo(x + 0.5, 0); ctx.lineTo(x + 0.5, H); ctx.stroke();
      if (hLabel) ctx.fillText(hLabel + " " + (Math.round(gh * 10) / 10), x + 3, H - 6);
    }
    for (var gv = Math.ceil(vVisible[0] / step) * step; gv <= vVisible[1]; gv += step) {
      var y = toPx(hMid, gv)[1];
      ctx.strokeStyle = Math.abs(gv % (step * 5)) < 1e-9 ? C["grid-strong"] : C.grid;
      ctx.beginPath(); ctx.moveTo(0, y + 0.5); ctx.lineTo(W, y + 0.5); ctx.stroke();
      ctx.fillText(vLabel + " " + (Math.round(gv * 10) / 10), 4, y - 3);
    }

    var clip = this.clip;
    var t = this.t;
    var self = this;

    // Blocks: placements and digging
    function blockMarker(b, color, active, filled) {
      var corners = flipV
        ? [{ x: b.x + 0.5, y: b.y, z: b.z + 0.5 }]
        : [{ x: b.x, y: b.y, z: b.z }, { x: b.x + 1, y: b.y, z: b.z + 1 }];
      ctx.globalAlpha = active ? 0.95 : 0.35;
      ctx.strokeStyle = color;
      ctx.fillStyle = color;
      ctx.lineWidth = active ? 2 : 1;
      var a, w, h;
      if (flipV) {
        var c = P(corners[0]);
        w = scale; h = scale;
        a = [c[0] - w / 2, c[1] - h];
      } else {
        var p0 = P(corners[0]), p1 = P(corners[1]);
        a = [Math.min(p0[0], p1[0]), Math.min(p0[1], p1[1])];
        w = Math.abs(p1[0] - p0[0]); h = Math.abs(p1[1] - p0[1]);
      }
      if (filled) {
        ctx.globalAlpha *= 0.45;
        ctx.fillRect(a[0], a[1], w, h);
        ctx.globalAlpha = active ? 0.95 : 0.35;
      }
      ctx.strokeRect(a[0] + 0.5, a[1] + 0.5, Math.max(1, w - 1), Math.max(1, h - 1));
      ctx.globalAlpha = 1;
    }
    clip.places.forEach(function (pl) {
      if (pl.block && pl.t <= t) blockMarker(pl.block, C.place, t - pl.t < 600, true);
    });
    clip.digs.forEach(function (d) {
      if (d.block && d.t <= t) blockMarker(d.block, C.dig, t - d.t < 600, d.action === "finish");
    });

    // Suspect full path (faint) and recent trail
    var pos = clip.positions;
    if (pos.length) {
      ctx.strokeStyle = C.suspect;
      ctx.lineWidth = 1;
      ctx.globalAlpha = 0.22;
      ctx.beginPath();
      pos.forEach(function (p, i) {
        var q = P(p);
        if (i === 0) ctx.moveTo(q[0], q[1]); else ctx.lineTo(q[0], q[1]);
      });
      ctx.stroke();
      ctx.globalAlpha = 1;
      ctx.lineWidth = 2;
      ctx.beginPath();
      var started = false;
      for (var i = Math.max(0, lastAt(pos, t - TRAIL_MS)); i < pos.length && pos[i].t <= t; i++) {
        var q = P(pos[i]);
        if (!started) { ctx.moveTo(q[0], q[1]); started = true; } else ctx.lineTo(q[0], q[1]);
      }
      ctx.stroke();
    }

    // Targets
    Object.keys(clip.targets).forEach(function (key) {
      var tg = clip.targets[key];
      var tp = tg.positions;
      if (!tp.length) return;
      ctx.strokeStyle = C.target;
      ctx.fillStyle = C.target;
      ctx.lineWidth = 1.5;
      ctx.globalAlpha = 0.7;
      ctx.beginPath();
      var started = false;
      for (var j = Math.max(0, lastAt(tp, t - TRAIL_MS)); j < tp.length && tp[j].t <= t; j++) {
        var q = P(tp[j]);
        if (!started) { ctx.moveTo(q[0], q[1]); started = true; } else ctx.lineTo(q[0], q[1]);
      }
      ctx.stroke();
      ctx.globalAlpha = 1;
      var cur = positionAt(tp, t);
      if (cur && t - tp[0].t > -50 && t - cur.t < 1500) {
        var c = P(cur);
        var w = tg.width * scale;
        ctx.lineWidth = 1.5;
        if (flipV) ctx.strokeRect(c[0] - w / 2, c[1] - tg.height * scale, w, tg.height * scale);
        else ctx.strokeRect(c[0] - w / 2, c[1] - w / 2, w, w);
        ctx.fillText(key.length > 18 ? key.slice(0, 8) + "…" : key, c[0] + w / 2 + 3, c[1] - 3);
      }
    });

    // Velocity arrows (x10 ticks) at the suspect position of the event
    clip.velocities.forEach(function (v) {
      if (v.t > t || t - v.t > 3000) return;
      var at = positionAt(pos, v.t);
      if (!at) return;
      var from = P(at);
      var to = P({ x: at.x + v.vx * 10, y: at.y + v.vy * 10, z: at.z + v.vz * 10 });
      ctx.globalAlpha = t - v.t < 800 ? 1 : 0.4;
      arrow(ctx, from, to, C.velocity);
      ctx.globalAlpha = 1;
    });

    // Teleports
    clip.teleports.forEach(function (tpe) {
      if (tpe.t > t) return;
      var q = P(tpe);
      ctx.globalAlpha = t - tpe.t < 1000 ? 1 : 0.4;
      ctx.strokeStyle = C.teleport;
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(q[0], q[1] - 6); ctx.lineTo(q[0] + 6, q[1]); ctx.lineTo(q[0], q[1] + 6); ctx.lineTo(q[0] - 6, q[1]); ctx.closePath();
      if (tpe.confirmed) { ctx.fillStyle = C.teleport; ctx.fill(); }
      ctx.stroke();
      ctx.globalAlpha = 1;
    });

    // Attacks: cross at the eye, dashed line to the target position at that time
    clip.attacks.forEach(function (a) {
      if (a.t > t || !a.eye) return;
      var recent = t - a.t < 1000;
      ctx.globalAlpha = recent ? 1 : 0.3;
      var e = P(a.eye);
      // Eye-to-target lines only for the last couple of seconds; older attacks keep just their marker.
      var tgt = t - a.t < TRAIL_MS ? positionAt(a.history.length ? a.history : (clip.targets[a.target] || { positions: [] }).positions, a.t) : null;
      if (tgt) {
        var tc = P({ x: tgt.x, y: tgt.y + a.height / 2, z: tgt.z });
        ctx.strokeStyle = C.attack;
        ctx.lineWidth = recent ? 1.5 : 1;
        ctx.setLineDash([4, 3]);
        ctx.beginPath(); ctx.moveTo(e[0], e[1]); ctx.lineTo(tc[0], tc[1]); ctx.stroke();
        ctx.setLineDash([]);
      }
      ctx.strokeStyle = C.attack;
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(e[0] - 5, e[1] - 5); ctx.lineTo(e[0] + 5, e[1] + 5);
      ctx.moveTo(e[0] + 5, e[1] - 5); ctx.lineTo(e[0] - 5, e[1] + 5);
      ctx.stroke();
      ctx.globalAlpha = 1;
    });

    // Suspect now: position, view ray
    var now = positionAt(pos, t);
    if (now) {
      var mv = clip.moves[lastAt(clip.moves, t)];
      var sneaking = mv ? (mv.flags & 2) !== 0 : false;
      var eye = { x: now.x, y: now.y + (sneaking ? EYE_HEIGHT_SNEAK : EYE_HEIGHT), z: now.z };
      var ri = lastAt(clip.rotations, t);
      var rot = ri >= 0 ? clip.rotations[ri] : null;
      var feet = P(now);
      var eyePx = P(eye);
      if (rot) {
        var d = viewDirection(rot.yaw, rot.pitch);
        var tip = P({ x: eye.x + d.x * RAY_LENGTH, y: eye.y + d.y * RAY_LENGTH, z: eye.z + d.z * RAY_LENGTH });
        ctx.strokeStyle = C.suspect;
        ctx.lineWidth = 2;
        ctx.beginPath(); ctx.moveTo(eyePx[0], eyePx[1]); ctx.lineTo(tip[0], tip[1]); ctx.stroke();
        ctx.beginPath(); ctx.arc(tip[0], tip[1], 2.5, 0, Math.PI * 2); ctx.fillStyle = C.suspect; ctx.fill();
      }
      ctx.fillStyle = C.suspect;
      ctx.strokeStyle = C.bg;
      ctx.lineWidth = 2;
      if (flipV) {
        var bw = 0.6 * scale, bh = (sneaking ? 1.5 : 1.8) * scale;
        ctx.globalAlpha = 0.35;
        ctx.fillRect(feet[0] - bw / 2, feet[1] - bh, bw, bh);
        ctx.globalAlpha = 1;
      }
      ctx.beginPath(); ctx.arc(flipV ? eyePx[0] : feet[0], flipV ? eyePx[1] : feet[1], 5, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
      ctx.fillStyle = C.text;
      ctx.fillText(clip.playerName || "suspect", feet[0] + 8, feet[1] + 12);
    }
    void self;
  };

  function arrow(ctx, from, to, color) {
    var dx = to[0] - from[0], dy = to[1] - from[1];
    var len = Math.sqrt(dx * dx + dy * dy);
    if (len < 2) return;
    ctx.strokeStyle = color;
    ctx.fillStyle = color;
    ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(from[0], from[1]); ctx.lineTo(to[0], to[1]); ctx.stroke();
    var ux = dx / len, uy = dy / len;
    ctx.beginPath();
    ctx.moveTo(to[0], to[1]);
    ctx.lineTo(to[0] - ux * 7 - uy * 4, to[1] - uy * 7 + ux * 4);
    ctx.lineTo(to[0] - ux * 7 + uy * 4, to[1] - uy * 7 - ux * 4);
    ctx.closePath();
    ctx.fill();
  }

  Viewer.prototype.drawTimeline = function () {
    var cv = this.timeline;
    var ctx = cv.getContext("2d");
    if (!ctx) return;
    var dpr = cv._dpr || 1;
    var W = cv.width / dpr, H = cv.height / dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var C = this.colors;
    var clip = this.clip;
    ctx.fillStyle = C.bg;
    ctx.fillRect(0, 0, W, H);
    var span = Math.max(1, clip.end - clip.start);
    var left = 8, right = W - 8;
    function X(t) {
      return left + ((t - clip.start) / span) * (right - left);
    }
    var rows = [
      [clip.attacks, C.attack],
      [clip.swings, C.suspect],
      [clip.places.concat(clip.digs), C.place],
      [clip.velocities.concat(clip.teleports), C.velocity],
      [clip.flags, C.flag],
    ];
    var rowH = (H - 18) / rows.length;
    rows.forEach(function (row, r) {
      ctx.fillStyle = row[1];
      var y = 4 + r * rowH;
      row[0].forEach(function (e) {
        ctx.fillRect(Math.round(X(e.t)) - 1, y, 2, Math.max(2, rowH - 2));
      });
    });
    // teleports drawn in their own colour on the velocity row
    ctx.fillStyle = C.teleport;
    clip.teleports.forEach(function (e) {
      ctx.fillRect(Math.round(X(e.t)) - 1, 4 + 3 * rowH, 2, Math.max(2, rowH - 2));
    });
    ctx.fillStyle = C.text;
    ctx.font = "10px system-ui, sans-serif";
    ctx.fillText("0 s", left, H - 4);
    var endLabel = (span / 1000).toFixed(1) + " s";
    ctx.fillText(endLabel, right - ctx.measureText(endLabel).width, H - 4);
    var px = X(this.t);
    ctx.strokeStyle = C.text;
    ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(px, 0); ctx.lineTo(px, H - 14); ctx.stroke();
  };

  Viewer.prototype.updateInfo = function () {
    var clip = this.clip;
    var t = this.t;
    var f = this.fields;
    var now = positionAt(clip.positions, t);
    if (f.pos) f.pos.textContent = now ? "x " + now.x.toFixed(2) + "  y " + now.y.toFixed(2) + "  z " + now.z.toFixed(2) : "—";
    var ri = lastAt(clip.rotations, t);
    var rot = ri >= 0 ? clip.rotations[ri] : null;
    if (f.rot) f.rot.textContent = rot ? rot.yaw.toFixed(1) + "° / " + rot.pitch.toFixed(1) + "°" : "—";
    var mi = lastAt(clip.moves, t);
    var mv = mi >= 0 ? clip.moves[mi] : null;
    if (f.move) {
      var parts = mv ? [mv.onGround ? "on ground" : "airborne"].concat(flagNames(mv.flags, MOVE_FLAGS)) : [];
      f.move.textContent = parts.length ? parts.join(", ") : "—";
    }
    var ei = lastAt(clip.envs, t);
    var env = ei >= 0 ? clip.envs[ei] : null;
    if (f.env) {
      var envNames = env ? flagNames(env.flags, ENV_FLAGS) : [];
      f.env.textContent = env
        ? (envNames.length ? envNames.join(", ") : "no special blocks") + (isFinite(env.tickMs) ? " · server tick " + env.tickMs.toFixed(1) + " ms" : "")
        : "—";
    }
    if (f.events) {
      var near = [];
      var W = 150;
      clip.attacks.forEach(function (a) {
        if (Math.abs(a.t - t) > W) return;
        var tgt = positionAt(a.history.length ? a.history : (clip.targets[a.target] || { positions: [] }).positions, a.t);
        var dist = "";
        if (tgt && a.eye) {
          var dx = tgt.x - a.eye.x, dy = tgt.y + a.height / 2 - a.eye.y, dz = tgt.z - a.eye.z;
          dist = " at " + Math.sqrt(dx * dx + dy * dy + dz * dz).toFixed(2) + " blocks (eye to target centre)";
        }
        near.push("attack on " + a.target + dist + (isFinite(a.pingMs) ? ", ping " + a.pingMs + " ms" : ""));
      });
      clip.swings.forEach(function (s) {
        if (Math.abs(s.t - t) <= W) near.push(s.offhand ? "swing (off hand)" : "swing");
      });
      clip.places.forEach(function (p) {
        if (Math.abs(p.t - t) <= W && p.block) near.push("place against " + p.block.x + " " + p.block.y + " " + p.block.z + " face " + p.face + (p.item ? " (" + p.item + ")" : ""));
      });
      clip.digs.forEach(function (d) {
        if (Math.abs(d.t - t) <= W && d.block) near.push("dig " + d.action + " " + d.block.x + " " + d.block.y + " " + d.block.z);
      });
      clip.velocities.forEach(function (v) {
        if (Math.abs(v.t - t) <= W) near.push((v.explosion ? "explosion " : "") + "velocity " + v.vx.toFixed(3) + " " + v.vy.toFixed(3) + " " + v.vz.toFixed(3));
      });
      clip.teleports.forEach(function (p) {
        if (Math.abs(p.t - t) <= W) near.push("teleport " + (p.confirmed ? "confirmed" : "sent") + " to " + p.x.toFixed(1) + " " + p.y.toFixed(1) + " " + p.z.toFixed(1));
      });
      clip.flags.forEach(function (fl) {
        if (Math.abs(fl.t - t) <= W) near.push("plugin flag " + fl.check + (fl.info ? ": " + fl.info : ""));
      });
      f.events.textContent = near.length ? near.slice(0, 6).join(" · ") : "—";
    }
  };

  root.PandaEyeReplay = {
    normalizeClip: normalizeClip,
    uuidOf: uuidOf,
    viewDirection: viewDirection,
    positionAt: positionAt,
    lastAt: lastAt,
  };

  if (root.document) {
    var start = function () {
      var els = root.document.querySelectorAll(".replay-viewer[data-clip-url]");
      for (var i = 0; i < els.length; i++) new Viewer(els[i]).load();
    };
    if (root.document.readyState === "loading") root.document.addEventListener("DOMContentLoaded", start);
    else start();
  }
})(typeof globalThis !== "undefined" ? globalThis : this);
