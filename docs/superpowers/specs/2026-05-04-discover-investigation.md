# `breezy discover` — investigation notes

**Date:** 2026-05-04
**Related commit:** `e5b9f93` (pkg/breezy: enumerate local interface broadcasts in Discover())
**Status:** code fix landed; environmental issue documented for the operator's awareness.

## Symptom

`breezy discover` returns `no Breezy devices found on the LAN` even though all three units are powered, reachable at known IPs (`192.168.1.148`, `.152`, `.160`), and respond to direct `breezy <name> status` calls.

## Two unrelated causes

The investigation found two distinct things going on. Cause (1) is a code defect, fixed in `e5b9f93`. Cause (2) is environmental, undated by the fix.

### Cause 1 — `Discover()` only broadcast to a hardcoded subnet list

Before `e5b9f93`, `pkg/breezy/discover.go`'s `DefaultBroadcasts` was a static slice:

```
255.255.255.255:4000
192.168.0.255:4000
192.168.1.255:4000
```

A host on any subnet *other* than `192.168.0.0/24` or `192.168.1.0/24` would not be sending a directed broadcast that reaches its own LAN. The all-ones broadcast (`255.255.255.255`) is reliable on a single-NIC host with a flat L2 segment, but is silently dropped by NAT boundaries and by some routers configured to suppress storm broadcasts.

**Fix (`e5b9f93`):** added `localBroadcasts()` which enumerates every up, non-loopback IPv4 interface and computes its directed-broadcast address. `Discover()` now sends to `DefaultBroadcasts ∪ localBroadcasts()`, deduped. A host on `10.42.0.0/16` will now send to `10.42.255.255:4000` automatically.

This is a strict improvement and lands in master. Run from a host on `192.168.1.0/24` and discovery should now reach the units regardless of whether the static list happened to cover that subnet.

### Cause 2 — this VM is QEMU-NAT'd off the home LAN

The host this project is developed on is at `10.0.2.15/24` — the classic QEMU user-mode NAT range. The user's ERV units are on `192.168.1.0/24`. Direct unicast traffic (e.g. `breezy playroom status` to `192.168.1.148:4000`) traverses the NAT fine because QEMU's slirp passes outbound UDP. **Broadcast traffic does not cross the NAT** — `255.255.255.255` is consumed by the NAT boundary, and a directed-broadcast packet to `192.168.1.255` is routed to slirp's gateway as ordinary unicast and dropped (routers don't forward directed broadcasts by default).

Symptom-side: even with the `e5b9f93` fix, `localBroadcasts()` from the VM will only ever return `10.0.2.255:4000` (its own NAT subnet). No path to the actual ERV LAN exists for broadcast.

**This is not a code bug.** It's a network-topology constraint of how the dev VM is configured. The breezy library is doing exactly the right thing — it just has nowhere to send broadcasts that can reach the units.

## What to do

To use `breezy discover` against the actual ERV fleet, pick one:

1. **Run discovery from a host that's actually on the device subnet.** If you have a Linux box, Pi, or workstation on `192.168.1.0/24`, copy `breezy` to it and run `breezy discover` there. The `e5b9f93` fix means it'll work on whatever subnet that host is on.

2. **Switch this VM to bridged networking.** In QEMU, a `-netdev bridge,br=br0,...` (instead of `-netdev user,...`) puts the VM on the same L2 segment as the home LAN; it gets a `192.168.1.x` address from the LAN's DHCP and can both unicast and broadcast to the units. This is a libvirt/QEMU configuration change, not a code change.

3. **Skip discovery.** The three device IDs are already known and recorded in the user's config. Discovery is a first-time-bootstrap convenience; the production workflow uses direct IPs and works fine over the QEMU NAT.

The code is correct; the environment is the limit.

## Verification gap

The `e5b9f93` fix has not been observed working against real hardware in this session, because (Cause 2) the VM cannot reach the units' subnet. If the operator runs `./breezy discover` from a host on `192.168.1.0/24` after this commit, the expected output is three lines, one per unit, with the right device IDs and IPs. If that doesn't happen, the next things to check are:

- A local firewall (`firewalld`, `ufw`, an `iptables` rule, or a switch ACL) that drops outbound UDP/4000 broadcasts or inbound replies.
- Per-port behaviour of the AP/router — some consumer routers isolate WiFi clients from the wired segment by default ("AP isolation"), which prevents broadcast discovery between WiFi-attached units and a wired PC.
- A read-deadline issue in `discoverInternal` if the units take more than ~2s to reply on a slow network.

None of those are speculative-fix-worthy without observation. Check them empirically if discovery still fails on a properly-attached host.
