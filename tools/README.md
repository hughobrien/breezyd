# probe.py

Interactive Twinfresh probe used during Phase 0 to characterize parameters.
Read or write a single parameter by hex ID against one device.

## Usage

    python3 tools/probe.py --ip 192.168.1.148 --id BREEZY00000000A0 read 0x25
    python3 tools/probe.py --ip 192.168.1.148 --id BREEZY00000000A0 read all
    python3 tools/probe.py --ip 192.168.1.148 --id BREEZY00000000A0 write 0x02 2

## Safety

The script refuses to write to credential and network params:
0x7D (protocol password), 0x95 (WiFi SSID), 0x96 (WiFi password),
0x9C-0x9F and 0xA3 (IP/mask/gateway/DNS).

Use 192.168.1.148 only during Phase 0. The other devices stay untouched until
the param table is trusted.
