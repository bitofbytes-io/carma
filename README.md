# Carma

A self-hosted vehicle maintenance tracker for the household, planned for
`https://carma.bitofbytes.io`. Track maintenance and repair records across every
car in the garage, attach receipts, get reminded when routine service is due, and
export the history when it's time to sell.

**Status: planning.** This repo currently contains the project plan and design
docs only - no application code yet.

## What it will do

- **Vehicles** - manage the whole household garage; every allowlisted user sees
  and edits the same vehicles.
- **Records** - log service/repairs with date, type, odometer, cost, vendor, and
  notes; search, sort, and filter the history.
- **Receipts** - attach PDF/photo receipts to any record; stored on the NAS, no
  more glove-box paper.
- **Reminders** - time- and/or mileage-based intervals per vehicle and service
  type; overdue and due-soon surfaced on the dashboard. Email reminders are a
  stretch goal.
- **Export** - filtered CSV (XLSX later) of any records view.

## How it will be built

Go monolith + HTMX (the dined pattern), single Docker image, Postgres on bahamut,
receipts on an NFS volume, deployed to the crystal1 Docker Swarm by the standard
GitHub Actions → Tailscale registry → bare-repo post-receive pipeline. Stack file,
Traefik route, and deploy hook will live in `home_swarm` as usual.

## Documentation

| Doc | Contents |
|---|---|
| [docs/PRD.md](docs/PRD.md) | Features, MVP vs stretch scope, data model, permissions, acceptance criteria |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Stack decisions, repo layout, storage, reminder engine, CI/CD flow |
| [docs/INFRA.md](docs/INFRA.md) | Naming, stack/Traefik/hook definitions for home_swarm, one-time bootstrap checklist |
| [docs/UI.md](docs/UI.md) | User flows, screen wireframes, HTMX interaction notes |
| [docs/mockups/](docs/mockups/) | Visual mockups of the key screens |

## Design preview

![Garage dashboard](docs/mockups/01-garage-dashboard.png)

![Vehicle detail](docs/mockups/02-vehicle-detail.png)
