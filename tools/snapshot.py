#!/usr/bin/env python3
"""Snapshot all readable Breezy params (pages 0, 3, 4) for a device.
Use to capture before/after states around an app setting change.

    python3 tools/snapshot.py --ip 192.168.1.148 --id BREEZY00000000A0 --pwd testpwd baseline.json
    # ... user changes one thing in the app ...
    python3 tools/snapshot.py --ip 192.168.1.148 --id BREEZY00000000A0 --pwd testpwd after.json
    python3 tools/snapshot.py diff baseline.json after.json
"""
import argparse, socket, json, sys, time

def build(devid, pwd, func, payload):
    body = bytes([0xFD, 0xFD, 0x02])
    body += bytes([len(devid)]) + devid.encode()
    body += bytes([len(pwd)]) + pwd
    body += bytes([func]) + payload
    cs = sum(body[2:]) & 0xFFFF
    return body + bytes([cs & 0xFF, (cs >> 8) & 0xFF])

def parse(data, devid, pwd):
    prefix = bytes([0xFD, 0xFD, 0x02, len(devid)]) + devid.encode() + bytes([len(pwd)]) + pwd
    return data[len(prefix):-2]

def read_param(s, ip, devid, pwd, payload):
    pkt = build(devid, pwd, 0x01, payload)
    s.sendto(pkt, (ip, 4000))
    try:
        data, _ = s.recvfrom(2048)
        return parse(data, devid, pwd)
    except socket.timeout:
        return None

def capture(ip, devid, pwd):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(('0.0.0.0', 0))
    s.settimeout(0.5)
    snap = {}
    # Page 0: 0x01..0xFB
    for pid in range(0x01, 0xFC):
        body = read_param(s, ip, devid, pwd.encode(), bytes([pid]))
        if not body or len(body) < 2 or body[0] != 0x06:
            continue
        # Skip "FD <id>" unsupported
        rest = body[1:]
        if len(rest) >= 2 and rest[0] == 0xFD:
            continue
        snap[f'0x{pid:02X}'] = rest.hex()
        time.sleep(0.015)
    # Pages 3 and 4: probe 0x00..0xFB via FF prefix
    for hi in (0x03, 0x04):
        for lo in range(0x00, 0xFC):
            body = read_param(s, ip, devid, pwd.encode(), bytes([0xFF, hi, lo]))
            if not body or len(body) < 2 or body[0] != 0x06:
                continue
            rest = body[1:]
            # Skip ff <hi> fd <lo> unsupported
            if len(rest) >= 4 and rest[0] == 0xFF and rest[1] == hi and rest[2] == 0xFD:
                continue
            snap[f'0x{hi:02X}{lo:02X}'] = rest.hex()
            time.sleep(0.012)
    return snap

def diff(a, b):
    keys = sorted(set(a.keys()) | set(b.keys()))
    changes = []
    for k in keys:
        va, vb = a.get(k), b.get(k)
        if va != vb:
            changes.append((k, va, vb))
    return changes

def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='cmd', required=True)

    pc = sub.add_parser('capture', help='capture a snapshot to file')
    pc.add_argument('--ip', required=True)
    pc.add_argument('--id', required=True)
    pc.add_argument('--pwd', default='testpwd')
    pc.add_argument('out')

    pd = sub.add_parser('diff', help='diff two snapshot files')
    pd.add_argument('a')
    pd.add_argument('b')

    a = p.parse_args()
    if a.cmd == 'capture':
        snap = capture(a.ip, a.id, a.pwd)
        with open(a.out, 'w') as f:
            json.dump(snap, f, indent=2, sort_keys=True)
        print(f'captured {len(snap)} params -> {a.out}')
    else:
        with open(a.a) as f: A = json.load(f)
        with open(a.b) as f: B = json.load(f)
        changes = diff(A, B)
        if not changes:
            print('no changes')
            return
        print(f'{len(changes)} changed param(s):')
        for k, va, vb in changes:
            print(f'  {k:8s}  {va or "(absent)":>20s}  ->  {vb or "(absent)"}')

if __name__ == '__main__':
    main()
