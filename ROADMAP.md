# Momagrid Roadmap

Planned features and improvements, roughly ordered by priority.
Items without a milestone are under consideration — not yet scheduled.

---

## Cluster / Hub-to-Hub

### Automatic Peer Discovery  _(not yet scheduled)_

**Current state:** peers are registered manually with `mg peer add <url>`.
This is intentional for the initial peering implementation — it keeps the
trust model explicit (you choose exactly which hubs to federate with).

**Planned:** add optional automatic discovery so hubs on the same LAN can
find each other without manual `mg peer add` steps.

Two mechanisms are worth exploring:

| Mechanism | Scope | Notes |
|-----------|-------|-------|
| mDNS / DNS-SD (e.g. `_momagrid._tcp.local`) | LAN only | Zero-config, no infrastructure; requires multicast |
| Rendezvous / registry server | WAN | Hubs register with a lightweight registry; peers pull the list |

Discovery should remain **opt-in** (`mg hub up --discover` or a config flag)
so that manually-peered, air-gapped, or privacy-sensitive deployments are
unaffected.

**Acceptance criteria:**
- Two hubs on the same LAN discover each other within 30 s of both starting
- Discovery respects a `--no-discover` flag / `discover: false` in config
- Manually-added peers still work alongside discovered ones

---

## Agent Management

_(future items go here)_

---

## Observability

_(future items go here)_
