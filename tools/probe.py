#!/usr/bin/env python3
"""Interactive Twinfresh probe. Read/write any param against one device.
Used during Phase 0 to characterize the param map empirically."""
import argparse, socket, sys, time

SAFETY_LOCKED = {0x7D, 0x95, 0x96, 0x9C, 0x9D, 0x9E, 0x9F, 0xA3}

def build(devid: str, pwd: bytes, func: int, payload: bytes) -> bytes:
    body = bytes([0xFD, 0xFD, 0x02])
    body += bytes([len(devid)]) + devid.encode()
    body += bytes([len(pwd)]) + pwd
    body += bytes([func]) + payload
    cs = sum(body[2:]) & 0xFFFF
    return body + bytes([cs & 0xFF, (cs >> 8) & 0xFF])

def parse_response(data: bytes, devid: str, pwd: bytes) -> bytes:
    """Strip the echoed header and trailing checksum, return the function+payload body."""
    prefix = bytes([0xFD, 0xFD, 0x02, len(devid)]) + devid.encode() + bytes([len(pwd)]) + pwd
    if not data.startswith(prefix):
        raise ValueError(f"unexpected header: {data[:len(prefix)].hex()}")
    return data[len(prefix):-2]  # strip checksum

def parse_param_value(body: bytes, want_id: int):
    """Walk a response body and find the value for want_id.
    Returns (id, value_bytes, full_chunk_bytes_for_logging)."""
    if not body or body[0] != 0x06:
        return (None, b"", body)
    body = body[1:]  # consume function code
    i = 0
    while i < len(body):
        b = body[i]
        if b == 0xFE:
            size = body[i + 1]
            pid = body[i + 2]
            val = body[i + 3 : i + 3 + size]
            chunk = body[i : i + 3 + size]
            if pid == want_id:
                return (pid, val, chunk)
            i += 3 + size
        elif b == 0xFD:
            pid = body[i + 1]
            chunk = body[i : i + 2]
            if pid == want_id:
                return (pid, b"", chunk)
            i += 2
        else:
            pid = b
            val = body[i + 1 : i + 2]
            chunk = body[i : i + 2]
            if pid == want_id:
                return (pid, val, chunk)
            i += 2
    return (None, b"", b"")

def send(sock, ip, devid, pwd, func, payload, timeout=1.5):
    sock.settimeout(timeout)
    sock.sendto(build(devid, pwd, func, payload), (ip, 4000))
    return sock.recvfrom(2048)[0]

def cmd_read(args, sock):
    if args.target == "all":
        for pid in range(0x01, 0xFC):
            try:
                resp = send(sock, args.ip, args.id, args.pwd.encode(), 0x01, bytes([pid]))
            except socket.timeout:
                continue
            body = parse_response(resp, args.id, args.pwd.encode())
            _, val, chunk = parse_param_value(body, pid)
            if chunk and not (len(chunk) == 2 and chunk[0] == 0xFD):
                print(f"  0x{pid:02X}: raw={chunk.hex()}  val_bytes={val.hex() or '(empty)'}  int={int.from_bytes(val, 'little') if val else 'n/a'}")
            time.sleep(0.02)
        return
    pid = int(args.target, 0)
    try:
        resp = send(sock, args.ip, args.id, args.pwd.encode(), 0x01, bytes([pid]))
    except socket.timeout:
        print(f"timeout reading 0x{pid:02X} from {args.ip}", file=sys.stderr)
        sys.exit(1)
    body = parse_response(resp, args.id, args.pwd.encode())
    _, val, chunk = parse_param_value(body, pid)
    print(f"0x{pid:02X} = raw={chunk.hex()}  val_bytes={val.hex() or '(empty)'}  int={int.from_bytes(val, 'little') if val else 'n/a'}")

def cmd_write(args, sock):
    pid = int(args.target, 0)
    if pid in SAFETY_LOCKED:
        print(f"refusing to write to safety-locked param 0x{pid:02X}", file=sys.stderr)
        sys.exit(2)
    val = int(args.value, 0)
    payload = bytes([pid, val & 0xFF])
    try:
        resp = send(sock, args.ip, args.id, args.pwd.encode(), 0x03, payload)
    except socket.timeout:
        print(f"timeout writing 0x{pid:02X} to {args.ip}", file=sys.stderr)
        sys.exit(1)
    print(f"write ack: {resp.hex()}")
    time.sleep(0.2)
    try:
        resp = send(sock, args.ip, args.id, args.pwd.encode(), 0x01, bytes([pid]))
    except socket.timeout:
        print(f"timeout reading back 0x{pid:02X}", file=sys.stderr)
        sys.exit(1)
    body = parse_response(resp, args.id, args.pwd.encode())
    _, val_bytes, chunk = parse_param_value(body, pid)
    print(f"after write 0x{pid:02X}: raw={chunk.hex()}  int={int.from_bytes(val_bytes, 'little') if val_bytes else 'n/a'}")

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--ip", required=True)
    p.add_argument("--id", required=True, help="16-char device ID")
    p.add_argument("--pwd", default="1111")
    sub = p.add_subparsers(dest="cmd", required=True)
    pr = sub.add_parser("read")
    pr.add_argument("target", help="hex id like 0x25, or 'all'")
    pr.set_defaults(func=cmd_read)
    pw = sub.add_parser("write")
    pw.add_argument("target", help="hex id like 0x02")
    pw.add_argument("value", help="integer (decimal or 0x..)")
    pw.set_defaults(func=cmd_write)
    args = p.parse_args()
    if len(args.id) != 16:
        sys.exit("device ID must be 16 chars")
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", 0))
    args.func(args, sock)

if __name__ == "__main__":
    main()
