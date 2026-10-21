#!/usr/bin/env python3
"""Find why objects of a class are still alive: shortest reference path(s) from a GC root, from an HPROF dump.

  jcmd <pid> GC.heap_dump /tmp/x.hprof
  python3 -I hprof-retainers.py /tmp/x.hprof dev.pandascript.pandaeye.e2efake.FakePandaEye [--max 3]

Used by the e2e harness when a plugin class loader is not collected after `plugman unload` (ARCHITECTURE 4.1:
"the class loader must be collectable"). Iterative backward search: each pass scans the dump once for
referrers of the current frontier; stops at GC roots. Pure stdlib, handles 4- and 8-byte ids.
"""
import mmap
import struct
import sys

BASIC = {2: None, 4: 1, 5: 2, 6: 4, 7: 8, 8: 1, 9: 2, 10: 4, 11: 8}  # type -> size (2 = object, id size)
ROOT_TAGS = {0xFF: 'unknown', 0x01: 'jni-global', 0x02: 'jni-local', 0x03: 'java-frame', 0x04: 'native-stack',
             0x05: 'sticky-class', 0x06: 'thread-block', 0x07: 'monitor', 0x08: 'thread-object'}


class Dump:
    def __init__(self, path):
        self.f = open(path, 'rb')
        self.m = mmap.mmap(self.f.fileno(), 0, access=mmap.ACCESS_READ)
        end = self.m.find(b'\0')
        self.idsize = struct.unpack_from('>I', self.m, end + 1)[0]
        self.start = end + 1 + 4 + 8
        self.idfmt = '>Q' if self.idsize == 8 else '>I'
        self.strings = {}
        self.class_names = {}      # class obj id -> name
        self.classes = {}          # class obj id -> (super, loader, instance field list [(name_id, type)], statics [(name_id, type, value)])
        self.segments = []         # (offset, length) of heap dump segments
        self._index()
        self.layout = {}

    def rid(self, off):
        return struct.unpack_from(self.idfmt, self.m, off)[0]

    def _index(self):
        m, off, n = self.m, self.start, len(self.m)
        name_of_class = {}
        while off < n:
            tag = m[off]
            length = struct.unpack_from('>I', m, off + 5)[0]
            body = off + 9
            if tag == 0x01:
                self.strings[self.rid(body)] = m[body + self.idsize: body + length].decode('utf-8', 'replace')
            elif tag == 0x02:
                cid = self.rid(body + 4)
                name_of_class[cid] = self.rid(body + 8 + self.idsize)
            elif tag in (0x0C, 0x1C):
                self.segments.append((body, length))
            off = body + length
        for cid, sid in name_of_class.items():
            self.class_names[cid] = self.strings.get(sid, '?').replace('/', '.')
        # class dumps
        for seg_off, seg_len in self.segments:
            p, end = seg_off, seg_off + seg_len
            while p < end:
                p = self._skip_sub(p, collect_classes=True)

    def _skip_sub(self, p, collect_classes=False, visit=None):
        m, I = self.m, self.idsize
        t = m[p]
        p += 1
        if t in (0xFF, 0x05, 0x07):
            if visit: visit('root', self.rid(p), ROOT_TAGS[t])
            return p + I
        if t == 0x01:
            if visit: visit('root', self.rid(p), 'jni-global')
            return p + 2 * I
        if t in (0x02, 0x03, 0x08):
            if visit: visit('root', self.rid(p), ROOT_TAGS[t])
            return p + I + 8
        if t in (0x04, 0x06):
            if visit: visit('root', self.rid(p), ROOT_TAGS[t])
            return p + I + 4
        if t == 0x20:
            cid = self.rid(p)
            sup = self.rid(p + I + 4)
            loader = self.rid(p + 2 * I + 4)
            q = p + 7 * I + 4 + 4
            ncp = struct.unpack_from('>H', m, q)[0]; q += 2
            for _ in range(ncp):
                ty = m[q + 2]; q += 3
                q += I if ty == 2 else BASIC[ty]
            nst = struct.unpack_from('>H', m, q)[0]; q += 2
            statics = []
            for _ in range(nst):
                nid = self.rid(q); ty = m[q + I]; q += I + 1
                if ty == 2:
                    statics.append((nid, self.rid(q))); q += I
                else:
                    q += BASIC[ty]
            nif = struct.unpack_from('>H', m, q)[0]; q += 2
            fields = []
            for _ in range(nif):
                fields.append((self.rid(q), m[q + I])); q += I + 1
            if collect_classes:
                self.classes[cid] = (sup, loader, fields, statics)
            if visit:
                visit('class', cid, (sup, loader, statics))
            return q
        if t == 0x21:
            oid = self.rid(p); cid = self.rid(p + I + 4)
            ln = struct.unpack_from('>I', m, p + 2 * I + 4)[0]
            data = p + 2 * I + 8
            if visit: visit('inst', oid, (cid, data))
            return data + ln
        if t == 0x22:
            oid = self.rid(p); cnt = struct.unpack_from('>I', m, p + I + 4)[0]
            data = p + 2 * I + 8
            if visit: visit('oarr', oid, (cnt, data))
            return data + cnt * I
        if t == 0x23:
            cnt = struct.unpack_from('>I', m, p + I + 4)[0]; ty = m[p + I + 8]
            return p + I + 9 + cnt * BASIC[ty]
        raise ValueError('unknown sub-record 0x%x at %d' % (t, p))

    def obj_offsets(self, cid):
        """Byte offsets of object fields inside an instance of class cid (own fields first, then supers)."""
        if cid in self.layout:
            return self.layout[cid]
        offs, pos, c = [], 0, cid
        names = []
        while c and c in self.classes:
            sup, _, fields, _ = self.classes[c]
            for nid, ty in fields:
                if ty == 2:
                    offs.append(pos); names.append(self.strings.get(nid, '?'))
                    pos += self.idsize
                else:
                    pos += BASIC[ty]
            c = sup
        self.layout[cid] = (offs, names)
        return self.layout[cid]


def main():
    if len(sys.argv) < 3:
        print(__doc__); sys.exit(2)
    d = Dump(sys.argv[1])
    target_class = sys.argv[2]
    maxpaths = int(sys.argv[sys.argv.index('--max') + 1]) if '--max' in sys.argv else 3
    tcids = {cid for cid, n in d.class_names.items() if n == target_class}
    if not tcids:
        print('class not found in dump:', target_class); return
    targets, roots, kind = set(), {}, {}

    def find_targets(k, oid, info):
        if k == 'inst' and info[0] in tcids: targets.add(oid)
        if k == 'root': roots.setdefault(oid, info)
        if k == 'inst': kind[oid] = info[0]
    for seg_off, seg_len in d.segments:
        p, end = seg_off, seg_off + seg_len
        while p < end:
            p = d._skip_sub(p, visit=find_targets)
    print(f'{len(targets)} live instance(s) of {target_class}; {len(roots)} gc roots')
    parent = {t: None for t in targets}
    frontier = set(targets)
    found = []
    for depth in range(40):
        hit = [o for o in frontier if o in roots]
        for o in hit:
            found.append(o)
        if len(found) >= maxpaths or not frontier:
            break
        nxt = set()

        def scan(k, oid, info):
            if oid in parent and parent.get(oid) is not None:
                return
            if k == 'inst':
                cid, data = info
                offs, names = d.obj_offsets(cid)
                for off, nm in zip(offs, names):
                    ref = d.rid(data + off)
                    if ref in frontier and oid not in parent:
                        parent[oid] = (ref, d.class_names.get(cid, '?') + '.' + nm)
                        nxt.add(oid)
            elif k == 'oarr':
                cnt, data = info
                for i in range(cnt):
                    ref = d.rid(data + i * d.idsize)
                    if ref in frontier and oid not in parent:
                        parent[oid] = (ref, f'Object[{i}]')
                        nxt.add(oid)
                        break
            elif k == 'class':
                sup, loader, statics = info
                for nid, ref in statics:
                    if ref in frontier and oid not in parent:
                        parent[oid] = (ref, 'static ' + d.class_names.get(oid, '?') + '.' + d.strings.get(nid, '?'))
                        nxt.add(oid)
                if loader in frontier and oid not in parent:
                    parent[oid] = (loader, 'class ' + d.class_names.get(oid, '?') + ' -> loader')
                    nxt.add(oid)
        for seg_off, seg_len in d.segments:
            p, end = seg_off, seg_off + seg_len
            while p < end:
                p = d._skip_sub(p, visit=scan)
        # instances also keep their class (and so its loader) alive: add class objects of frontier instances
        for o in frontier:
            c = kind.get(o)
        frontier = nxt
        print(f'  depth {depth + 1}: {len(frontier)} referrers')
    for r in found[:maxpaths]:
        print(f'\nGC root ({roots[r]}):')
        o = r
        while o is not None:
            nm = d.class_names.get(kind.get(o), d.class_names.get(o, '?'))
            p = parent.get(o)
            print(f'  {nm} @{o:x}' + (f'  --[{p[1]}]-->' if p else '   <== target'))
            o = p[0] if p else None
    if not found:
        print('no GC root reached (increase depth or the objects are only finalizer-reachable)')


if __name__ == '__main__':
    main()
